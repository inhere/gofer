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

const trackerPrimePrefix = "gofer repo prime --hook-json"
const trackerPrimeCommand = trackerPrimePrefix

func trackerPrimeCommandFor(agent string) string {
	return trackerPrimePrefix + " --agent " + agent
}

// InstallTrackerPrime merges only the tracker SessionStart command. A migration
// may also replace the old bd prime command without disturbing other hooks.
func InstallTrackerPrime(agent, root string, replaceBd bool) (bool, error) {
	path, err := ConfigFileFor(agent, root)
	if err != nil {
		return false, err
	}
	doc := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return false, fmt.Errorf("invalid hook JSON %s: %w", path, err)
		}
	}
	hookMap, _ := doc["hooks"].(map[string]any)
	if hookMap == nil {
		hookMap = map[string]any{}
	}
	changed := false
	if replaceBd {
		// Older bd setups use a plain `bd prime` (no --hook-json) and also hang
		// it on PreCompact; any of them would keep injecting bd's rules, so every
		// bd prime command goes, on every event. SessionStart fires again after a
		// compaction, so the gofer prime installed below covers that case too.
		for event, rawEntries := range hookMap {
			list, _ := rawEntries.([]any)
			if list == nil {
				continue
			}
			kept, removed := dropBdPrime(list)
			if !removed {
				continue
			}
			changed = true
			if len(kept) == 0 {
				delete(hookMap, event)
			} else {
				hookMap[event] = kept
			}
		}
	}
	entries, _ := hookMap["SessionStart"].([]any)
	found := false
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		hooks, _ := group["hooks"].([]any)
		for _, hook := range hooks {
			item, _ := hook.(map[string]any)
			if cmd, _ := item["command"].(string); cmd == trackerPrimeCommandFor(agent) {
				found = true
			} else if cmd == trackerPrimePrefix {
				// DEPRECATED(v0.81.0): remove upgrade of unqualified prime in v0.84.0.
				item["command"] = trackerPrimeCommandFor(agent)
				found, changed = true, true
			}
		}
	}
	if !found {
		entries = append(entries, map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": trackerPrimeCommandFor(agent)}}})
		changed = true
	}
	if !changed {
		return false, nil
	}
	hookMap["SessionStart"] = entries
	doc["hooks"] = hookMap
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, append(out, '\n'), 0o644)
}

// RemoveTrackerPrime removes only gofer's SessionStart memory command. Relay
// hooks and foreign entries remain untouched, so --remove --prime-only is safe
// to use on a configuration that also has the full hook installation.
func RemoveTrackerPrime(agent, root string) (bool, error) {
	path, err := ConfigFileFor(agent, root)
	if err != nil {
		return false, err
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	doc := map[string]any{}
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return false, fmt.Errorf("invalid hook JSON %s: %w", path, err)
		}
	}
	hookMap, _ := doc["hooks"].(map[string]any)
	entries, _ := hookMap["SessionStart"].([]any)
	kept, removed := dropTrackerPrime(entries)
	if !removed {
		return false, nil
	}
	if len(kept) == 0 {
		delete(hookMap, "SessionStart")
	} else {
		hookMap["SessionStart"] = kept
	}
	if len(hookMap) == 0 {
		delete(doc, "hooks")
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

func dropTrackerPrime(groups []any) (kept []any, removed bool) {
	for _, entry := range groups {
		group, _ := entry.(map[string]any)
		hooks, _ := group["hooks"].([]any)
		keptHooks := make([]any, 0, len(hooks))
		for _, hook := range hooks {
			item, _ := hook.(map[string]any)
			cmd, _ := item["command"].(string)
			if cmd == trackerPrimeCommandFor(AgentClaude) || cmd == trackerPrimeCommandFor(AgentCodex) || cmd == trackerPrimePrefix {
				removed = true
				continue
			}
			keptHooks = append(keptHooks, hook)
		}
		if len(keptHooks) == 0 && len(hooks) > 0 {
			continue
		}
		if group != nil {
			group["hooks"] = keptHooks
		}
		kept = append(kept, entry)
	}
	return kept, removed
}

// dropBdPrime removes every hook whose command is `bd prime` (with or without
// flags) from one event's groups, dropping groups that end up empty.
func dropBdPrime(groups []any) (kept []any, removed bool) {
	for _, entry := range groups {
		group, _ := entry.(map[string]any)
		hooks, _ := group["hooks"].([]any)
		keptHooks := make([]any, 0, len(hooks))
		for _, hook := range hooks {
			item, _ := hook.(map[string]any)
			cmd, _ := item["command"].(string)
			if cmd == "bd prime" || strings.HasPrefix(cmd, "bd prime ") {
				removed = true
				continue
			}
			keptHooks = append(keptHooks, hook)
		}
		if len(keptHooks) == 0 && len(hooks) > 0 {
			continue
		}
		if group != nil {
			group["hooks"] = keptHooks
		}
		kept = append(kept, entry)
	}
	return kept, removed
}

func HasTrackerPrime(agent, root string) bool {
	path, err := ConfigFileFor(agent, root)
	if err != nil {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	hookMap, _ := doc["hooks"].(map[string]any)
	entries, _ := hookMap["SessionStart"].([]any)
	for _, entry := range entries {
		group, _ := entry.(map[string]any)
		hooks, _ := group["hooks"].([]any)
		for _, hook := range hooks {
			item, _ := hook.(map[string]any)
			// DEPRECATED(v0.81.0): recognize the old command once so upgrades stay idempotent; remove in v0.84.0.
			if cmd, _ := item["command"].(string); cmd == trackerPrimeCommandFor(agent) || cmd == trackerPrimePrefix {
				return true
			}
		}
	}
	return false
}

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
	case AgentOmp:
		return hooks.OmpExtension, nil
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
	case AgentOmp:
		return filepath.Join(dir, ".omp", "extensions", ompExtensionFile), nil
	case AgentJcode:
		// jcode has no project-level hook config: <dir> is a jcode home (JCODE_HOME)
		// whose config.toml holds the [hooks] table.
		return filepath.Join(dir, "config.toml"), nil
	}
	return "", fmt.Errorf("hookrelay: unsupported agent %q", agent)
}

// GlobalConfigFileFor returns the user-level hook config for agent given the
// user's home directory: the same file ConfigFileFor names for Claude / Codex,
// ~/.omp/agent/extensions/ for omp (PI_CODING_AGENT_DIR overrides ~/.omp/agent)
// and ~/.jcode/config.toml for jcode (JCODE_HOME overrides ~/.jcode).
func GlobalConfigFileFor(agent, home string) (string, error) {
	switch agent {
	case AgentOmp:
		base := strings.TrimSpace(os.Getenv("PI_CODING_AGENT_DIR"))
		if base == "" {
			base = filepath.Join(home, ".omp", "agent")
		}
		return filepath.Join(base, "extensions", ompExtensionFile), nil
	case AgentJcode:
		base := strings.TrimSpace(os.Getenv("JCODE_HOME"))
		if base == "" {
			base = filepath.Join(home, ".jcode")
		}
		return filepath.Join(base, "config.toml"), nil
	}
	return ConfigFileFor(agent, home)
}

// HasRelayHooks reports whether dir (a project root, or the home dir for
// the user level when global is false-equivalent callers pass the path from
// ConfigFileFor / GlobalConfigFileFor) already holds gofer's relay hooks for agent.
func HasRelayHooks(agent, dir string) bool {
	path, err := ConfigFileFor(agent, dir)
	if err != nil {
		return false
	}
	return hasRelayHooksAt(agent, path)
}

// HasRelayHooksAt is HasRelayHooks for an explicit config file path.
func HasRelayHooksAt(agent, path string) bool { return hasRelayHooksAt(agent, path) }

func hasRelayHooksAt(agent, path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	switch agent {
	case AgentOmp:
		return bytes.Contains(raw, []byte(OmpExtensionMarker))
	case AgentJcode:
		for _, line := range strings.Split(string(raw), "\n") {
			if _, val, ok := jcodeHookLine(line); ok && isOurJcodeCommand(val) {
				return true
			}
		}
		return false
	}
	var doc map[string]any
	if json.Unmarshal(raw, &doc) != nil {
		return false
	}
	hookMap, _ := doc["hooks"].(map[string]any)
	for _, entries := range hookMap {
		list, _ := entries.([]any)
		if _, dropped := stripOurs(list); dropped > 0 {
			return true
		}
	}
	return false
}

// Install merges agent's embedded hook fragment into the JSON file at path
// (created when absent): every gofer-owned entry is first dropped, then the
// template entries appended, so the call is idempotent and never duplicates.
// Other tools' hook entries and unrelated top-level keys are preserved. With
// remove=true only the drop happens. force lets an unparsable existing file be
// replaced instead of aborting.
func Install(agent, path string, remove, force bool) (InstallResult, error) {
	switch agent {
	case AgentOmp:
		return installOmp(path, remove, force)
	case AgentJcode:
		return installJcode(path, remove, force)
	}
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
	case AgentOmp:
		return []string{
			"omp hook 是 TypeScript 扩展: 已写入 gofer-relay.ts, 新启动的 omp 会话自动加载 (用 /extensions 确认)",
			"omp 的 handler 上限 30s, 中继等待在后台子进程里进行, web 回复会作为新一轮用户消息送回",
		}
	case AgentJcode:
		return []string{
			"jcode 的 hook 是 fire-and-forget: 会话登记/状态/最后一条消息/工具进度可见, 但无法 web 回复注入 (传话请用 tmux 注入 --deliver)",
			"jcode 无项目级 hook 配置: 已写入 [hooks] 的 session_start/turn_start/turn_end/post_tool/session_end",
		}
	case AgentClaude:
		return []string{
			"Stop hook 等待期间终端显示 hook 运行中; 回到电脑想直接输入可按 Esc 取消等待",
		}
	}
	return nil
}
