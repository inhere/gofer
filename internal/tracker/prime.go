package tracker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

const PrimeMaxBytes = 8 << 10

// WorkPrimeHint is the one-line SessionStart hint about work-item reports (W1): a session
// that is asked to report its work item knows the command. It lives in the fixed header
// so it always fits the byte budget.
const WorkPrimeHint = "工作项：被要求汇报时运行 `gofer work report <id> --goal … --status … --blocker … --next …`（`gofer work ls` 可查 id）。"

// TrackerPrimeHint is the one-line command memory aid in the fixed header: how to
// pick up work and how to recall memories that the summaries below only abbreviate.
const TrackerPrimeHint = "任务：`gofer issue ready|show <id>|update <id> --claim|comment <id> \"…\"|close <id>`；记忆：`gofer memory ls <关键字>`（搜 key+内容）/ `show <key>`（全文）/ `set <key> \"…\"`。"

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
		return "按功能点本地提交是默认授权；不要 push；`.gofer/tracker/*.jsonl` 的变化随功能点一起提交；提交前 `git status` 确认没有夹带无关文件。", nil
	case "ask":
		return "提交前先询问用户；不要 push；`.gofer/tracker/*.jsonl` 的变化随获批功能点一起提交。", nil
	case "none":
		return "本仓库不提交本次改动；不要 push。", nil
	default:
		return "", fmt.Errorf("unknown commit_policy %q", policy)
	}
}

func (s *Store) Prime() (string, error) {
	sections, err := s.primeSections()
	if err != nil {
		return "", err
	}
	full := sections.render(sections.active, sections.ready, sections.memory)
	if len([]byte(full)) <= PrimeMaxBytes {
		return full, nil
	}
	const notice = "\n[内容已截断：使用 gofer issue ls / gofer memory show 查看全部]\n"
	budget := PrimeMaxBytes - len([]byte(sections.render(nil, nil, nil))) - len([]byte(notice))
	if budget < 0 {
		return "", fmt.Errorf("commit policy exceeds prime limit")
	}
	used := 0
	choose := func(lines []string) []string {
		selected := make([]string, 0, len(lines))
		for _, line := range lines {
			if used+len([]byte(line)) <= budget {
				selected = append(selected, line)
				used += len([]byte(line))
			}
		}
		return selected
	}
	// Issue rows come first: they are capped by their own limits (10 + 10 lines)
	// and are what a session needs to resume, so a pile of memory summaries must
	// never crowd them out (a migrated bd repository can carry dozens of
	// memories). Memory takes whatever budget is left, newest first. The
	// sections still render in their normal reading order.
	active := choose(sections.active)
	ready := choose(sections.ready)
	memory := choose(sections.memory)
	return sections.render(active, ready, memory) + notice, nil
}

// PrimeEstimate reports the untruncated local context size. Server-backed scoped
// memories and handoffs are best effort and are deliberately outside this estimate.
func (s *Store) PrimeEstimate() (bytes int, truncated bool, err error) {
	sections, err := s.primeSections()
	if err != nil {
		return 0, false, err
	}
	bytes = len([]byte(sections.render(sections.active, sections.ready, sections.memory)))
	return bytes, bytes > PrimeMaxBytes, nil
}

type primeSections struct {
	policy      string
	activeTitle string
	readyTitle  string
	memoryTitle string
	active      []string
	ready       []string
	memory      []string
}

func (p primeSections) render(active, ready, memory []string) string {
	var out strings.Builder
	out.WriteString("## 提交策略\n")
	out.WriteString(p.policy)
	out.WriteString("\n")
	out.WriteString(WorkPrimeHint)
	out.WriteString("\n")
	out.WriteString(TrackerPrimeHint)
	out.WriteString("\n")
	for _, group := range []struct {
		title string
		lines []string
	}{{p.activeTitle, active}, {p.readyTitle, ready}, {p.memoryTitle, memory}} {
		if group.title == "" {
			continue
		}
		out.WriteString("\n## ")
		out.WriteString(group.title)
		out.WriteString("\n")
		for _, line := range group.lines {
			out.WriteString(line)
		}
	}
	return out.String()
}

func (s *Store) primeSections() (primeSections, error) {
	cfg, err := s.ReadConfig()
	if err != nil {
		return primeSections{}, err
	}
	policy, err := CommitPolicyText(cfg.CommitPolicy)
	if err != nil {
		return primeSections{}, err
	}
	sections := primeSections{policy: policy}
	if cfg.Prime.IssuesEnabled() {
		sections.activeTitle = "进行中/已认领 issue"
		issues, readErr := s.ReadIssues()
		if readErr != nil {
			return primeSections{}, readErr
		}
		active := make([]Issue, 0)
		for _, item := range issues {
			if item.Status == "in_progress" || (item.Assignee != "" && item.Status != "closed") {
				active = append(active, item)
			}
		}
		sort.Slice(active, func(i, j int) bool {
			if active[i].Priority != active[j].Priority {
				return active[i].Priority < active[j].Priority
			}
			if active[i].UpdatedAt != active[j].UpdatedAt {
				return active[i].UpdatedAt > active[j].UpdatedAt
			}
			return active[i].ID < active[j].ID
		})
		for i, item := range active {
			if i >= cfg.Prime.ActiveLimit() {
				break
			}
			sections.active = append(sections.active, fmt.Sprintf("- %s [%s] %s\n", item.ID, item.Status, item.Title))
		}
		if len(active) > cfg.Prime.ActiveLimit() {
			sections.active = append(sections.active, fmt.Sprintf("共 %d 条，`gofer issue ls` 查看全部\n", len(active)))
		}
	}
	if cfg.Prime.ReadyEnabled() {
		sections.readyTitle = fmt.Sprintf("ready 前 %d", cfg.Prime.ReadyCount())
		ready, readErr := s.Ready()
		if readErr != nil {
			return primeSections{}, readErr
		}
		for i, item := range ready {
			if i >= cfg.Prime.ReadyCount() {
				break
			}
			sections.ready = append(sections.ready, fmt.Sprintf("- %s P%d %s\n", item.ID, item.Priority, item.Title))
		}
	}
	if cfg.Prime.MemoryEnabled() {
		sections.memoryTitle = "memory"
		memories, readErr := s.ReadMemories()
		if readErr != nil {
			return primeSections{}, readErr
		}
		sort.Slice(memories, func(i, j int) bool {
			if memories[i].UpdatedAt != memories[j].UpdatedAt {
				return memories[i].UpdatedAt > memories[j].UpdatedAt
			}
			return memories[i].Key < memories[j].Key
		})
		summaries, omitted := 0, 0
		for _, item := range memories {
			full := PrimeMemoryFull(item.Tags, "")
			if !full && cfg.Prime.SummaryLimit() >= 0 && summaries >= cfg.Prime.SummaryLimit() {
				omitted++
				continue
			}
			sections.memory = append(sections.memory, PrimeMemoryLine(item.Key, item.Content, item.Tags, ""))
			if !full {
				summaries++
			}
		}
		if summaries > 0 {
			sections.memory = append(sections.memory, "全文：`gofer memory show <key>`\n")
		}
		if omitted > 0 {
			sections.memory = append(sections.memory, fmt.Sprintf("另有 %d 条记忆未列出：`gofer memory ls <关键字>` 搜索\n", omitted))
		}
	}
	return sections, nil
}

func PrimeMemoryFull(tags []string, agentName string) bool {
	for _, tag := range tags {
		if tag == "prime" || (agentName != "" && tag == "agent:"+agentName) {
			return true
		}
	}
	return false
}

// PrimeMemoryLine applies the same tag and summary rule to local and scoped memory.
func PrimeMemoryLine(key, content string, tags []string, agentName string) string {
	if PrimeMemoryFull(tags, agentName) {
		return fmt.Sprintf("- %s: %s\n", key, content)
	}
	first := strings.TrimSpace(strings.SplitN(content, "\n", 2)[0])
	runes := []rune(first)
	available := 80 - len([]rune("- "+key+": "))
	if available <= 0 {
		first = ""
	} else if len(runes) > available {
		first = string(runes[:available-1]) + "…"
	}
	return fmt.Sprintf("- %s: %s\n", key, first)
}

// PrimeRule is the subset included in a dispatched job's mandatory rules.
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
	sort.Slice(memories, func(i, j int) bool { return memories[i].UpdatedAt > memories[j].UpdatedAt })
	var out strings.Builder
	out.WriteString("提交策略：")
	out.WriteString(policy)
	out.WriteString("\nmemory：\n")
	for _, item := range memories {
		line := fmt.Sprintf("- %s: %s\n", item.Key, item.Content)
		if out.Len()+len([]byte(line)) > PrimeMaxBytes-64 {
			out.WriteString("[memory 已截断，优先保留最新]\n")
			break
		}
		out.WriteString(line)
	}
	return out.String(), nil
}
