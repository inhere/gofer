package bdmigrate

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// HookPlan is the planned rewrite of one agent hook config file.
type HookPlan struct {
	Agent   string   `json:"agent"`
	Path    string   `json:"path"`
	Exists  bool     `json:"exists"`
	Changes []string `json:"changes,omitempty"`
	Manual  []string `json:"manual,omitempty"` // bd commands left in place that need a human
	New     []byte   `json:"-"`                // full new file content; nil when nothing changes
}

var bdWordRe = regexp.MustCompile(`(^|[\s;&|(])bd(\.exe)?(\s|$)`)

func isBdPrime(cmd string) bool {
	f := strings.Fields(cmd)
	return len(f) >= 2 && (f[0] == "bd" || f[0] == "bd.exe") && f[1] == "prime"
}

func isBdCodexHook(cmd string) bool {
	f := strings.Fields(cmd)
	return len(f) >= 2 && (f[0] == "bd" || f[0] == "bd.exe") && f[1] == "codex-hook"
}

func isGoferPrime(cmd string) bool {
	return strings.HasPrefix(strings.TrimSpace(cmd), "gofer repo prime")
}

func goferPrimeCommand(agent string) string {
	return "gofer repo prime --hook-json --agent " + agent
}

// planHooks rewrites the agent's hook config: every `bd prime ...` /
// `bd codex-hook ...` entry goes; the first one on SessionStart is replaced in
// place (same group, same matcher) by the gofer prime, so the layout of the
// file stays. Other events (PreCompact ...) lose the bd entry and get nothing,
// because the gofer prime emits SessionStart-shaped JSON and SessionStart
// fires again after a compaction anyway. Foreign hooks and key order are kept.
func planHooks(agent, path string) (HookPlan, error) {
	plan := HookPlan{Agent: agent, Path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	plan.Exists = true
	var doc any = newOrdObj()
	if len(bytes.TrimSpace(raw)) > 0 {
		if doc, err = parseOrdered(raw); err != nil {
			return plan, fmt.Errorf("invalid hook JSON %s: %w", path, err)
		}
	}
	root, ok := doc.(*ordObj)
	if !ok {
		return plan, fmt.Errorf("hook JSON %s is not an object", path)
	}
	hooksVal, _ := root.get("hooks")
	hooksObj, _ := hooksVal.(*ordObj)
	if hooksObj == nil {
		hooksObj = newOrdObj()
		root.set("hooks", hooksObj)
	}
	want := goferPrimeCommand(agent)
	havePrime := false
	for _, event := range hooksObj.keys {
		for _, h := range commandHooks(hooksObj.vals[event]) {
			if cmd, _ := h.get("command"); isGoferPrime(asString(cmd)) && event == "SessionStart" {
				havePrime = true
			}
		}
	}
	changed := false
	for _, event := range append([]string(nil), hooksObj.keys...) {
		groups, _ := hooksObj.vals[event].([]any)
		var keptGroups []any
		eventChanged := false
		for _, g := range groups {
			group, _ := g.(*ordObj)
			if group == nil {
				keptGroups = append(keptGroups, g)
				continue
			}
			list, _ := group.vals["hooks"].([]any)
			var keptHooks []any
			removedHere := 0
			for _, h := range list {
				hook, _ := h.(*ordObj)
				if hook == nil {
					keptHooks = append(keptHooks, h)
					continue
				}
				cmdVal, _ := hook.get("command")
				cmd := asString(cmdVal)
				switch {
				case isBdPrime(cmd) || isBdCodexHook(cmd):
					changed, eventChanged = true, true
					removedHere++
					if event == "SessionStart" && !havePrime {
						fresh := newOrdObj()
						fresh.set("command", want)
						fresh.set("type", "command")
						keptHooks = append(keptHooks, fresh)
						havePrime = true
						plan.Changes = append(plan.Changes, fmt.Sprintf("%s: `%s` -> `%s`", event, cmd, want))
					} else {
						plan.Changes = append(plan.Changes, fmt.Sprintf("%s: removed `%s`", event, cmd))
					}
				default:
					if bdWordRe.MatchString(cmd) {
						plan.Manual = append(plan.Manual, fmt.Sprintf("%s hook command still calls bd: `%s`", event, cmd))
					}
					keptHooks = append(keptHooks, h)
				}
			}
			if removedHere > 0 && len(keptHooks) == 0 {
				continue // the whole group only held bd hooks
			}
			if removedHere > 0 {
				group.set("hooks", keptHooks)
			}
			keptGroups = append(keptGroups, g)
		}
		if eventChanged {
			if len(keptGroups) == 0 {
				hooksObj.del(event)
			} else {
				hooksObj.set(event, keptGroups)
			}
		}
	}
	if !havePrime {
		entry := newOrdObj()
		hook := newOrdObj()
		hook.set("command", want)
		hook.set("type", "command")
		entry.set("hooks", []any{hook})
		entry.set("matcher", "")
		var groups []any
		if cur, ok := hooksObj.get("SessionStart"); ok {
			groups, _ = cur.([]any)
		}
		hooksObj.set("SessionStart", append(groups, entry))
		changed = true
		plan.Changes = append(plan.Changes, fmt.Sprintf("SessionStart: added `%s`", want))
	}
	if !changed {
		return plan, nil
	}
	indent := detectIndent(raw)
	out, err := marshalOrdered(root, indent)
	if err != nil {
		return plan, err
	}
	if bytes.HasSuffix(raw, []byte("\n")) || len(bytes.TrimSpace(raw)) == 0 {
		out = append(out, '\n')
	}
	plan.New = out
	return plan, nil
}

func commandHooks(groups any) []*ordObj {
	var out []*ordObj
	list, _ := groups.([]any)
	for _, g := range list {
		group, _ := g.(*ordObj)
		if group == nil {
			continue
		}
		hooks, _ := group.vals["hooks"].([]any)
		for _, h := range hooks {
			if hook, _ := h.(*ordObj); hook != nil {
				out = append(out, hook)
			}
		}
	}
	return out
}

func asString(v any) string { s, _ := v.(string); return s }

// hookFile is the per-agent project hook config path under root.
func hookFile(root, agent string) string {
	if agent == "codex" {
		return filepath.Join(root, ".codex", "hooks.json")
	}
	return filepath.Join(root, ".claude", "settings.json")
}
