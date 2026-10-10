package tracker

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Memory flags (design 2026-10-10-handoff-brief-and-knowledge-loop §二): an agent that
// finds an injected memory out of date flags it instead of silently working around it.
// The flag only marks the memory 「⚠ 待复核」 wherever it is injected; a rule keeps its
// full text. Rewriting the content counts as the review and clears every flag.

// MemoryFlagsMax caps the flags a memory keeps (newest first).
const MemoryFlagsMax = 5

// memoryFlagReasonRunes caps the reason shown inside the 待复核 prefix.
const memoryFlagReasonRunes = 60

// MemoryFlagHint is the one-line prompt hint (prime rules area, dispatched-job rules)
// that tells an agent to flag a stale memory rather than bypass it.
const MemoryFlagHint = "发现注入的记忆 / 规则与实际不符时运行 `gofer memory flag <key> --reason \"…\"`（全局 / 项目记忆加 `--global` / `--project <p>`），不要静默绕过。"

// MemoryFlag is one "this memory looks wrong" report.
type MemoryFlag struct {
	At     string `json:"at"`
	By     string `json:"by,omitempty"`
	Job    string `json:"job,omitempty"`
	Reason string `json:"reason"`
}

// NewMemoryFlag builds a flag stamped at now; the reason is required.
func NewMemoryFlag(reason, by, jobID string, now time.Time) (MemoryFlag, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return MemoryFlag{}, errors.New("--reason is required: say what no longer matches")
	}
	return MemoryFlag{At: now.UTC().Format(time.RFC3339), By: strings.TrimSpace(by), Job: strings.TrimSpace(jobID), Reason: reason}, nil
}

// AddMemoryFlag puts flag first and keeps at most MemoryFlagsMax flags.
func AddMemoryFlag(flags []MemoryFlag, flag MemoryFlag) []MemoryFlag {
	out := make([]MemoryFlag, 0, MemoryFlagsMax)
	out = append(out, flag)
	for _, f := range flags {
		if len(out) >= MemoryFlagsMax {
			break
		}
		out = append(out, f)
	}
	return out
}

// MemoryFlagPrefix is 「⚠ 待复核（<latest reason>）」 for a flagged memory, else "".
func MemoryFlagPrefix(meta MemoryMeta) string {
	if len(meta.Flags) == 0 {
		return ""
	}
	return fmt.Sprintf("⚠ 待复核（%s）", truncateRunes(strings.Join(strings.Fields(meta.Flags[0].Reason), " "), memoryFlagReasonRunes))
}

// FlagMemory adds a flag to a repository memory. It leaves updated_at alone: a flag is
// a report about the content, not a change of it.
func (s *Store) FlagMemory(key string, flag MemoryFlag) (Memory, error) {
	return s.updateMemoryFlags(key, func(flags []MemoryFlag) []MemoryFlag { return AddMemoryFlag(flags, flag) })
}

// UnflagMemory clears every flag of a repository memory (the human review).
func (s *Store) UnflagMemory(key string) (Memory, error) {
	return s.updateMemoryFlags(key, func([]MemoryFlag) []MemoryFlag { return nil })
}

func (s *Store) updateMemoryFlags(key string, change func([]MemoryFlag) []MemoryFlag) (Memory, error) {
	var out Memory
	err := s.UpdateMemories(func(items []Memory) ([]Memory, error) {
		for i := range items {
			if items[i].Key == key {
				items[i].Flags = change(items[i].Flags)
				out = items[i]
				return items, nil
			}
		}
		return nil, fmt.Errorf("memory %s not found", key)
	})
	return out, err
}
