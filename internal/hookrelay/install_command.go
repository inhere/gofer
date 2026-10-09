package hookrelay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The PreToolUse entry of the command-time memory injection (design 2026-10-09
// §2.9, P5). The full relay install (Install) carries it in the embedded
// templates; the memory-only installs (`gofer repo init`, `gofer init hooks
// --prime-only`) add just this entry beside the SessionStart prime.

const commandMemoryEvent = "PreToolUse"

// commandMemoryMatcher is the PreToolUse matcher per agent (the shell tools).
func commandMemoryMatcher(agent string) string {
	if agent == AgentCodex {
		return "Bash|shell|shell_command|exec_command"
	}
	return "Bash"
}

func readHookDoc(path string) (map[string]any, error) {
	doc := map[string]any{}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) != 0 {
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("invalid hook JSON %s: %w", path, err)
		}
	}
	return doc, nil
}

func writeHookDoc(path string, doc map[string]any) error {
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// InstallCommandMemory adds gofer's PreToolUse shell entry (`gofer hook
// <agent>`) to the claude / codex hook config under root unless a gofer
// PreToolUse entry is already there (the full relay install has one).
// Idempotent; every other entry is left alone. Reports whether it wrote.
func InstallCommandMemory(agent, root string) (bool, error) {
	if agent != AgentClaude && agent != AgentCodex {
		return false, nil
	}
	path, err := ConfigFileFor(agent, root)
	if err != nil {
		return false, err
	}
	doc, err := readHookDoc(path)
	if err != nil {
		return false, err
	}
	hookMap, _ := doc["hooks"].(map[string]any)
	if hookMap == nil {
		hookMap = map[string]any{}
	}
	entries, _ := hookMap[commandMemoryEvent].([]any)
	if _, ours := stripOurs(entries); ours > 0 {
		return false, nil
	}
	hookMap[commandMemoryEvent] = append(entries, map[string]any{
		"matcher": commandMemoryMatcher(agent),
		"hooks":   []any{map[string]any{"type": "command", "command": ourCommandPrefix + " " + agent, "timeout": 5}},
	})
	doc["hooks"] = hookMap
	return true, writeHookDoc(path, doc)
}

// RemoveCommandMemory drops gofer's PreToolUse entries under root, but only when
// no other gofer relay hook is installed there (then they belong to the full
// relay install, which `gofer init hooks --remove` takes out). Reports whether
// it wrote.
func RemoveCommandMemory(agent, root string) (bool, error) {
	if agent != AgentClaude && agent != AgentCodex {
		return false, nil
	}
	path, err := ConfigFileFor(agent, root)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	doc, err := readHookDoc(path)
	if err != nil {
		return false, err
	}
	hookMap, _ := doc["hooks"].(map[string]any)
	for event, entries := range hookMap {
		if event == commandMemoryEvent {
			continue
		}
		list, _ := entries.([]any)
		if _, ours := stripOurs(list); ours > 0 {
			return false, nil
		}
	}
	entries, _ := hookMap[commandMemoryEvent].([]any)
	kept, ours := stripOurs(entries)
	if ours == 0 {
		return false, nil
	}
	if len(kept) == 0 {
		delete(hookMap, commandMemoryEvent)
	} else {
		hookMap[commandMemoryEvent] = kept
	}
	if len(hookMap) == 0 {
		delete(doc, "hooks")
	}
	return true, writeHookDoc(path, doc)
}
