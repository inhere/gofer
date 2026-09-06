package hookrelay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/hooks"
)

// ourCommandPrefix identifies hook entries owned by gofer inside a user's
// hook config, so install/remove never touch entries from other tools.
const ourCommandPrefix = "gofer hook"

// InstallResult reports what the merge changed.
type InstallResult struct {
	Path     string
	Created  bool
	Added    int // gofer entries written
	Replaced int // previous gofer entries dropped before re-adding
	Removed  int // entries removed (--remove)
	Notes    []string
}

// TemplateFor returns the embedded hook fragment for agent.
func TemplateFor(agent string) ([]byte, error) {
	switch agent {
	case AgentClaude:
		return hooks.ClaudeSettings, nil
	case AgentCodex:
		return hooks.CodexHooks, nil
	}
	return nil, fmt.Errorf("hookrelay: no hook template for agent %q", agent)
}

// ConfigFileFor returns the hook config file for agent under dir: Claude Code
// reads hooks from <dir>/.claude/settings.json, Codex from <dir>/.codex/hooks.json.
func ConfigFileFor(agent, dir string) (string, error) {
	switch agent {
	case AgentClaude:
		return filepath.Join(dir, ".claude", "settings.json"), nil
	case AgentCodex:
		return filepath.Join(dir, ".codex", "hooks.json"), nil
	}
	return "", fmt.Errorf("hookrelay: unsupported agent %q", agent)
}

// Install merges agent's embedded hook fragment into the JSON file at path
// (created when absent): every gofer-owned entry is first dropped, then the
// template entries appended, so the call is idempotent and never duplicates.
// Other tools' hook entries and unrelated top-level keys are preserved. With
// remove=true only the drop happens. force lets an unparsable existing file be
// replaced instead of aborting.
func Install(agent, path string, remove, force bool) (InstallResult, error) {
	tmpl, err := TemplateFor(agent)
	if err != nil {
		return InstallResult{}, err
	}
	var want map[string]any
	if err := json.Unmarshal(tmpl, &want); err != nil {
		return InstallResult{}, fmt.Errorf("hookrelay: embedded template for %s is invalid: %w", agent, err)
	}
	res := InstallResult{Path: path}
	doc := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(bytes.TrimSpace(raw)) > 0 {
			if uerr := json.Unmarshal(raw, &doc); uerr != nil {
				if !force {
					return res, fmt.Errorf("%s is not valid JSON (%v); fix it or pass --force to replace it", path, uerr)
				}
				res.Notes = append(res.Notes, "existing file was not valid JSON and has been replaced (--force)")
				doc = map[string]any{}
			}
		}
	case errors.Is(err, os.ErrNotExist):
		if remove {
			res.Notes = append(res.Notes, "nothing to remove: file does not exist")
			return res, nil
		}
		res.Created = true
	default:
		return res, fmt.Errorf("read %s: %w", path, err)
	}

	// hooks.<Event>[] merge.
	hooksAny, _ := doc["hooks"].(map[string]any)
	if hooksAny == nil {
		hooksAny = map[string]any{}
	}
	wantHooks, _ := want["hooks"].(map[string]any)
	for event, wantEntries := range wantHooks {
		existing, _ := hooksAny[event].([]any)
		kept, dropped := stripOurs(existing)
		if remove {
			res.Removed += dropped
		} else {
			res.Replaced += dropped
			wl, _ := wantEntries.([]any)
			kept = append(kept, wl...)
			res.Added += len(wl)
		}
		if len(kept) == 0 {
			delete(hooksAny, event)
		} else {
			hooksAny[event] = kept
		}
	}
	// Also strip gofer entries from events the template no longer lists.
	for event, entries := range hooksAny {
		if _, inTemplate := wantHooks[event]; inTemplate {
			continue
		}
		list, _ := entries.([]any)
		kept, dropped := stripOurs(list)
		if dropped > 0 {
			res.Removed += dropped
			if len(kept) == 0 {
				delete(hooksAny, event)
			} else {
				hooksAny[event] = kept
			}
		}
	}
	if len(hooksAny) == 0 {
		delete(doc, "hooks")
	} else {
		doc["hooks"] = hooksAny
	}

	// env merge (Claude template): set absent keys; never clobber a user value.
	if wantEnv, ok := want["env"].(map[string]any); ok {
		env, _ := doc["env"].(map[string]any)
		if env == nil {
			env = map[string]any{}
		}
		for k, v := range wantEnv {
			cur, has := env[k]
			switch {
			case remove:
				if has && fmt.Sprint(cur) == fmt.Sprint(v) {
					delete(env, k)
				}
			case !has:
				env[k] = v
			case fmt.Sprint(cur) != fmt.Sprint(v):
				res.Notes = append(res.Notes, fmt.Sprintf("env %s already set to %v (kept; template wants %v)", k, cur, v))
			}
		}
		if len(env) == 0 {
			delete(doc, "env")
		} else {
			doc["env"] = env
		}
	}

	if remove && len(doc) == 0 && !res.Created {
		// Leave an empty object rather than deleting the user's file.
		doc = map[string]any{}
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return res, fmt.Errorf("encode %s: %w", path, err)
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return res, fmt.Errorf("mkdir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return res, fmt.Errorf("write %s: %w", path, err)
	}
	return res, nil
}

// stripOurs removes gofer-owned hook entries from a hooks.<Event> array. An
// entry mixing gofer and foreign hooks keeps the foreign ones. dropped counts
// gofer entries (or partial entries) removed.
func stripOurs(entries []any) (kept []any, dropped int) {
	for _, e := range entries {
		m, ok := e.(map[string]any)
		if !ok {
			kept = append(kept, e)
			continue
		}
		hs, _ := m["hooks"].([]any)
		var foreign []any
		ours := 0
		for _, h := range hs {
			hm, ok := h.(map[string]any)
			if ok && isOurCommand(hm["command"]) {
				ours++
				continue
			}
			foreign = append(foreign, h)
		}
		if ours == 0 {
			kept = append(kept, e)
			continue
		}
		dropped++
		if len(foreign) > 0 {
			nm := map[string]any{}
			for k, v := range m {
				nm[k] = v
			}
			nm["hooks"] = foreign
			kept = append(kept, nm)
		}
	}
	return kept, dropped
}

func isOurCommand(v any) bool {
	s, ok := v.(string)
	if !ok {
		return false
	}
	s = strings.TrimSpace(s)
	return s == ourCommandPrefix || strings.HasPrefix(s, ourCommandPrefix+" ")
}

// PostInstallNotes are the agent-specific reminders printed after install.
func PostInstallNotes(agent string) []string {
	switch agent {
	case AgentCodex:
		return []string{
			"Codex 需启用 hooks 特性: config.toml 中 [features] hooks = true (旧版本键名 codex_hooks)",
			"项目层 .codex/ 需先在 Codex 里 trust 该项目, 否则 hooks.json 不会加载",
		}
	case AgentClaude:
		return []string{
			"Stop hook 等待期间终端显示 hook 运行中; 回到电脑想直接输入可按 Esc 取消等待",
		}
	}
	return nil
}
