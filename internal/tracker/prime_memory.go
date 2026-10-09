package tracker

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// memoryViewOptions is the scene a memory list is rendered for.
type memoryViewOptions struct {
	CwdRel   string
	CwdKnown bool
	Now      time.Time
	// AgentName keeps the legacy `agent:<name>` full display of scoped memories.
	AgentName string
}

// memoryView renders memories as prime segments: rules in full, the rest as a
// grouped one-line index, handoffs separately. shown records which keys were
// already written in full so the index does not repeat them.
type memoryView struct {
	opts  memoryViewOptions
	items []Memory
	shown map[string]bool
}

func newMemoryView(items []Memory, opts memoryViewOptions) *memoryView {
	sorted := append([]Memory(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	return &memoryView{opts: opts, items: sorted, shown: map[string]bool{}}
}

// PathMatched reports whether the session cwd falls under the memory's when.paths.
func (v *memoryView) pathMatched(m Memory) bool {
	return v.opts.CwdKnown && MemoryMatchesPath(m.MemoryMeta, v.opts.CwdRel)
}

// wantsFull is the full-content rule (§2.3): kind=rule without `when`, or whose
// `when.paths` matches the cwd (keywords / commands are matched by hooks, P1b /
// P5); plus the legacy `agent:<name>` tag for the named agent.
func (v *memoryView) wantsFull(m Memory) bool {
	if v.opts.AgentName != "" && hasTag(m.Tags, "agent:"+v.opts.AgentName) {
		return true
	}
	if m.EffectiveKind() != MemoryKindRule {
		return false
	}
	return m.When.empty() || v.pathMatched(m)
}

func fullMemoryLine(m Memory) string { return fmt.Sprintf("- %s: %s\n", m.Key, m.Content) }

// rulesSegment writes full rules in key order within budget. Rules that do not
// fit drop to the index (marked 规则) with a note asking to trim them.
func (v *memoryView) rulesSegment(budget int) primeSegment {
	seg := primeSegment{budget: -1}
	var lines []string
	used := 0
	skipped := 0
	const note = "规则超出预算，%d 条未展开（见索引），请精简规则\n"
	reserve := len(fmt.Sprintf(note, 99)) + len("\n## 规则\n")
	for _, m := range v.items {
		if !v.wantsFull(m) {
			continue
		}
		line := fullMemoryLine(m)
		if budget >= 0 && used+len(line)+reserve > budget {
			skipped++
			continue
		}
		used += len(line)
		lines = append(lines, line)
		v.shown[m.Key] = true
	}
	if len(lines) == 0 && skipped == 0 {
		return seg
	}
	seg.title = "规则"
	seg.lines = lines
	seg.extra = skipped
	seg.more = func(n int) string { return fmt.Sprintf(note, n) }
	return seg
}

// MemoryIndexGroup is the index group of a memory: its first tag (ignoring the
// legacy `prime` and `agent:*` tags), else 其他.
func MemoryIndexGroup(tags []string) string {
	for _, tag := range tags {
		if tag == "prime" || strings.HasPrefix(tag, "agent:") {
			continue
		}
		return tag
	}
	return "其他"
}

func (v *memoryView) indexLine(m Memory, withGroup bool) string {
	var b strings.Builder
	b.WriteString("- ")
	if withGroup {
		b.WriteString("[" + MemoryIndexGroup(m.Tags) + "] ")
	}
	b.WriteString(m.Key)
	switch m.EffectiveKind() {
	case MemoryKindRule:
		b.WriteString("（规则）")
	case MemoryKindHandoff:
		b.WriteString("（交接）")
	}
	if summary := DisplayMemorySummary(m.MemoryMeta, m.Content); summary != "" {
		b.WriteString(" · " + summary)
	}
	if age := AgeText(m.UpdatedAt, v.opts.Now); age != "" {
		b.WriteString(" · " + age)
	}
	if MemoryStale(m.MemoryMeta, m.Tags, m.UpdatedAt, v.opts.Now) {
		b.WriteString("（久未更新）")
	}
	if v.pathMatched(m) {
		b.WriteString("（命中当前目录）")
	}
	b.WriteString("\n")
	return b.String()
}

// indexEntries orders the index: groups holding a cwd match first, then groups
// by their newest entry (其他 last); inside a group matches first, then newest.
func (v *memoryView) indexEntries(include func(Memory) bool) []Memory {
	groups := map[string][]Memory{}
	for _, m := range v.items {
		if v.shown[m.Key] || !include(m) {
			continue
		}
		g := MemoryIndexGroup(m.Tags)
		groups[g] = append(groups[g], m)
	}
	type groupInfo struct {
		name    string
		matched bool
		newest  string
	}
	infos := make([]groupInfo, 0, len(groups))
	for name, items := range groups {
		sort.SliceStable(items, func(i, j int) bool {
			mi, mj := v.pathMatched(items[i]), v.pathMatched(items[j])
			if mi != mj {
				return mi
			}
			if items[i].UpdatedAt != items[j].UpdatedAt {
				return items[i].UpdatedAt > items[j].UpdatedAt
			}
			return items[i].Key < items[j].Key
		})
		info := groupInfo{name: name, newest: items[0].UpdatedAt}
		for _, m := range items {
			info.matched = info.matched || v.pathMatched(m)
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool {
		a, b := infos[i], infos[j]
		if a.matched != b.matched {
			return a.matched
		}
		if (a.name == "其他") != (b.name == "其他") {
			return b.name == "其他"
		}
		if a.newest != b.newest {
			return a.newest > b.newest
		}
		return a.name < b.name
	})
	out := make([]Memory, 0, len(v.items))
	for _, info := range infos {
		out = append(out, groups[info.name]...)
	}
	return out
}

// indexSegment is the grouped one-line index of everything not shown in full
// except handoffs. limit < 0 is unlimited (prime.memory_summary_limit).
func (v *memoryView) indexSegment(title string, limit, budget int) primeSegment {
	entries := v.indexEntries(func(m Memory) bool { return m.EffectiveKind() != MemoryKindHandoff })
	if len(entries) == 0 {
		return primeSegment{}
	}
	seg := primeSegment{
		title:  fmt.Sprintf("%s（%d 条，按需 `gofer memory show <key>`）", title, len(entries)),
		budget: budget,
		more:   func(n int) string { return fmt.Sprintf("另有 %d 条：`gofer memory ls <关键字>`\n", n) },
	}
	for i, m := range entries {
		if limit >= 0 && i >= limit {
			seg.extra = len(entries) - i
			break
		}
		seg.lines = append(seg.lines, v.indexLine(m, true))
	}
	return seg
}

// liveHandoffs are the unexpired handoffs, newest first (cwd matches first).
func (v *memoryView) liveHandoffs() []Memory {
	var out []Memory
	for _, m := range v.items {
		if v.shown[m.Key] || m.EffectiveKind() != MemoryKindHandoff || MemoryExpired(m.MemoryMeta, m.Tags, m.UpdatedAt, v.opts.Now) {
			continue
		}
		out = append(out, m)
	}
	sort.SliceStable(out, func(i, j int) bool {
		mi, mj := v.pathMatched(out[i]), v.pathMatched(out[j])
		if mi != mj {
			return mi
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (v *memoryView) handoffLine(m Memory) string {
	line := strings.TrimSuffix(v.indexLine(m, false), "\n")
	if at, ok := MemoryExpiresAt(m.MemoryMeta, m.Tags, m.UpdatedAt); ok {
		line += fmt.Sprintf(" · %d 天后过期", ageDays(v.opts.Now, at))
	}
	return line + "\n"
}

// handoffSegment lists the newest unexpired handoff memories (§2.1). Expired
// ones stay out of prime; `memory ls` marks them 已过期.
func (v *memoryView) handoffSegment(budget int) primeSegment {
	live := v.liveHandoffs()
	if len(live) == 0 {
		return primeSegment{}
	}
	seg := primeSegment{
		title:  fmt.Sprintf("交接（未过期，最新 %d 条）", primeHandoffLimit),
		budget: budget,
		more:   func(n int) string { return fmt.Sprintf("另有 %d 条：`gofer memory ls --kind handoff`\n", n) },
	}
	for i, m := range live {
		if i >= primeHandoffLimit {
			seg.extra = len(live) - i
			break
		}
		seg.lines = append(seg.lines, v.handoffLine(m))
	}
	return seg
}

// MemoryForAgent filters `agent:<name>` tagged memories: untagged memories are
// for every agent, tagged ones only for the named agents.
func MemoryForAgent(tags []string, agentName string) bool {
	matched := false
	for _, tag := range tags {
		if strings.HasPrefix(tag, "agent:") {
			matched = true
			if strings.TrimPrefix(tag, "agent:") == strings.TrimSpace(agentName) {
				return true
			}
		}
	}
	return !matched
}

// ScopedPrimeOptions renders a server global / project memory section.
type ScopedPrimeOptions struct {
	AgentName    string
	CwdRel       string
	CwdKnown     bool
	Now          time.Time
	SummaryLimit int
	Budget       int
	// LsHint is the `gofer memory ls …` command for the omitted-entries note.
	LsHint string
}

// RenderScopedPrimeSection applies the local memory rules to scoped memories in
// one budgeted section: full rules (and `agent:<name>` memories) first, then the
// grouped index with the newest unexpired handoffs.
func RenderScopedPrimeSection(title string, items []Memory, opts ScopedPrimeOptions) string {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	visible := make([]Memory, 0, len(items))
	for _, m := range items {
		if MemoryForAgent(m.Tags, opts.AgentName) {
			visible = append(visible, m)
		}
	}
	view := newMemoryView(visible, memoryViewOptions{CwdRel: opts.CwdRel, CwdKnown: opts.CwdKnown, Now: opts.Now, AgentName: opts.AgentName})
	hint := opts.LsHint
	if hint == "" {
		hint = "gofer memory ls"
	}
	seg := primeSegment{title: title, budget: opts.Budget, more: func(n int) string {
		return fmt.Sprintf("另有 %d 条：`%s <关键字>`\n", n, hint)
	}}
	for _, m := range view.items {
		if view.wantsFull(m) {
			seg.lines = append(seg.lines, fullMemoryLine(m))
			view.shown[m.Key] = true
		}
	}
	entries := view.indexEntries(func(m Memory) bool { return m.EffectiveKind() != MemoryKindHandoff })
	if len(entries) > 0 {
		seg.lines = append(seg.lines, "索引（按需 `gofer memory show <key>`）：\n")
	}
	for i, m := range entries {
		if opts.SummaryLimit >= 0 && i >= opts.SummaryLimit {
			seg.extra += len(entries) - i
			break
		}
		seg.lines = append(seg.lines, view.indexLine(m, true))
	}
	live := view.liveHandoffs()
	for i, m := range live {
		if i >= primeHandoffLimit {
			seg.extra += len(live) - i
			break
		}
		seg.lines = append(seg.lines, view.handoffLine(m))
	}
	if len(seg.lines) == 0 {
		return ""
	}
	var out strings.Builder
	seg.write(&out)
	return out.String()
}
