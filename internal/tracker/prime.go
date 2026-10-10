package tracker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const PrimeMaxBytes = 8 << 10

// WorkPrimeHint is the one-line SessionStart hint about work-item reports (W1): a session
// that is asked to report its work item knows the command. It lives in the fixed header
// so it always fits the byte budget.
const WorkPrimeHint = "工作项：被要求汇报时运行 `gofer work report <id> --goal … --status … --blocker … --next …`（`gofer work ls` 可查 id）。"

// TrackerPrimeHint is the one-line command memory aid in the fixed header: how to
// pick up work and how to recall memories that the summaries below only abbreviate.
const TrackerPrimeHint = "任务：`gofer issue ready|show <id>|update <id> --claim|comment <id> \"…\"|close <id>`；记忆：`gofer memory ls <关键字>`（搜 key+摘要+内容）/ `show <key>`（全文）/ `set <key> \"…\" --summary \"一句话\"`；tracker 改动用 `gofer repo status --changed` 看，不要 diff `.gofer/tracker/*.jsonl`。\n" + MemoryWriteHint

// PrimeTakeoverHint is the last line of the local prime: the entry point for taking
// over an issue or a plan (design 2026-10-10 §一 「prime 配合」).
const PrimeTakeoverHint = "接手：issue 用 `gofer issue brief <id>`，plan 用 `gofer plan brief <id>`（上下文树、设计稿、相关提交、job / plan 与适用记忆一次拿齐）。"

// MemoryWriteHint is the writing guidance (design §2.8) shared by prime and the
// managed AGENTS block.
const MemoryWriteHint = "写记忆：长期约定用 `--kind rule`（写现状不写进度）；阶段进度写 plan 交接说明或 `--kind handoff`（默认 14 天后过期）；正文超 200 字要 `--summary`。"

// PrimeWithHandoffSection appends the caller-provided best-effort server handoff
// section while preserving the existing prime bytes first. The caller is expected
// to have applied project selection, timeout and ordering before this seam.
func (s *Store) PrimeWithHandoffSection(handoff string) (string, error) {
	base, err := s.Prime()
	if err != nil {
		return "", err
	}
	return appendHandoffSection(base, handoff), nil
}

// PrimeWithHandoffFetch runs a best-effort handoff lookup supplied by the command
// layer. The callback owns server discovery/authentication; tracker only applies
// the two-second budget and the existing handoff-only truncation seam.
func (s *Store) PrimeWithHandoffFetch(ctx context.Context, fetch func(context.Context) (string, error)) (string, error) {
	base, err := s.Prime()
	if err != nil {
		return "", err
	}
	if fetch == nil {
		return base, nil
	}
	result := make(chan struct {
		section string
		err     error
	}, 1)
	go func() {
		section, err := fetch(ctx)
		result <- struct {
			section string
			err     error
		}{section, err}
	}()
	var section string
	select {
	case <-ctx.Done():
		return base, nil
	case fetched := <-result:
		section = fetched.section
		if fetched.err != nil {
			return base, nil
		}
	}
	if strings.TrimSpace(section) == "" {
		return base, nil
	}
	return appendHandoffSection(base, section), nil
}

func appendHandoffSection(base, handoff string) string {
	section := "\n## 进行中 plan 的交接说明\n\n" + handoff
	if len([]byte(base))+len([]byte(section)) <= PrimeMaxBytes {
		return base + section
	}
	const marker = "\n[交接说明已截断]\n"
	remaining := PrimeMaxBytes - len([]byte(base)) - len([]byte("\n## 进行中 plan 的交接说明\n\n")) - len([]byte(marker))
	if remaining < 0 {
		return base
	}
	return base + "\n## 进行中 plan 的交接说明\n\n" + utf8Prefix(handoff, remaining) + marker
}

func utf8Prefix(value string, byteLimit int) string {
	if byteLimit <= 0 {
		return ""
	}
	if len(value) <= byteLimit {
		return value
	}
	cut := value[:byteLimit]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// AppendPrimeSections appends optional server-backed sections within the same
// bounded prime budget. Each section must include its heading and body.
func AppendPrimeSections(base string, sections ...string) string {
	out := base
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		remaining := PrimeMaxBytes - len([]byte(out))
		if remaining <= 0 {
			return out
		}
		if len([]byte(section)) <= remaining {
			out += section
			continue
		}
		marker := "\n[全局/项目记忆已截断]\n"
		keep := remaining - len([]byte(marker))
		if keep > 0 {
			out += utf8Prefix(section, keep) + marker
		} else {
			out += utf8Prefix(marker, remaining)
		}
		return out
	}
	return out
}

func CommitPolicyText(policy string) (string, error) {
	switch policy {
	case "", "local-commit":
		return "按功能点本地提交是默认授权；push 到远端需用户授权；`.gofer/tracker/*.jsonl` 的变化随功能点一起提交；提交前 `git status` 确认没有夹带无关文件。", nil
	case "ask":
		return "提交前先询问用户；push 到远端需用户授权；`.gofer/tracker/*.jsonl` 的变化随获批功能点一起提交。", nil
	case "none":
		return "本仓库不提交本次改动；push 到远端需用户授权。", nil
	default:
		return "", fmt.Errorf("unknown commit_policy %q", policy)
	}
}

// PrimeRule is the tracker part of a dispatched job's mandatory rules: the
// commit policy, every kind=rule memory in full (by key), then a one-line index
// of the other live memories (newest first) so the job knows what it can
// `gofer memory show`. Expired handoffs and archived memories are left out. The
// whole body stays within PrimeMaxBytes-64; rules that do not fit drop to the
// index, index lines that do not fit are counted.
func (s *Store) PrimeRule() (string, error) {
	cfg, err := s.ReadConfig()
	if err != nil {
		return "", err
	}
	policy, err := CommitPolicyText(cfg.CommitPolicy)
	if err != nil {
		return "", err
	}
	memories, err := s.ReadMemories()
	if err != nil {
		return "", err
	}
	return renderPrimeRule(policy, memories, time.Now()), nil
}

func renderPrimeRule(policy string, memories []Memory, now time.Time) string {
	const limit = PrimeMaxBytes - 64
	var rules, others []Memory
	for _, m := range memories {
		switch {
		case m.EffectiveKind() == MemoryKindRule:
			rules = append(rules, m)
		case MemoryExpired(m.MemoryMeta, m.Tags, m.UpdatedAt, now):
		default:
			others = append(others, m)
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].Key < rules[j].Key })
	var out strings.Builder
	out.WriteString("提交策略：")
	out.WriteString(policy)
	out.WriteString("\n")
	if len(rules) > 0 {
		out.WriteString("规则（全文）：\n")
	}
	// Reserve room for the flag hint, the index heading and its truncation note.
	reserve := 160 + len(MemoryFlagHint) + 1
	for _, m := range rules {
		key := m.Key
		if flag := MemoryFlagPrefix(m.MemoryMeta); flag != "" {
			key = flag + " " + key
		}
		line := fmt.Sprintf("- %s: %s\n", key, m.Content)
		if out.Len()+len(line)+reserve > limit {
			others = append(others, m)
			continue
		}
		out.WriteString(line)
	}
	if len(memories) > 0 {
		out.WriteString(MemoryFlagHint + "\n")
	}
	sort.SliceStable(others, func(i, j int) bool {
		if others[i].UpdatedAt != others[j].UpdatedAt {
			return others[i].UpdatedAt > others[j].UpdatedAt
		}
		return others[i].Key < others[j].Key
	})
	if len(others) == 0 {
		return out.String()
	}
	out.WriteString("其他记忆（索引，按需 `gofer memory show <key>`）：\n")
	for i, m := range others {
		line := "- " + m.Key
		if flag := MemoryFlagPrefix(m.MemoryMeta); flag != "" {
			line = "- " + flag + " " + m.Key
		}
		switch m.EffectiveKind() {
		case MemoryKindRule:
			line += "（规则）"
		case MemoryKindHandoff:
			line += "（交接）"
		}
		if summary := DisplayMemorySummary(m.MemoryMeta, m.Content); summary != "" {
			line += " · " + summary
		}
		line += "\n"
		if out.Len()+len(line)+64 > limit {
			fmt.Fprintf(&out, "[另有 %d 条：`gofer memory ls`]\n", len(others)-i)
			break
		}
		out.WriteString(line)
	}
	return out.String()
}
