package tracker

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Segment budgets of the local prime (design §2.3.1). Each segment truncates on
// its own and says how many entries it left out, so a long segment can never push
// the next one out of the 8KB total.
const (
	primeFocusBudget   = 600
	primeRulesBudget   = 3 << 10
	primeActiveBudget  = 600
	primeReadyBudget   = 800
	primeIndexBudget   = 1536
	primeHandoffBudget = 600
	// primeHandoffLimit is how many unexpired handoff memories prime lists.
	primeHandoffLimit = 3
	// primeStaleClaim marks an in_progress issue without updates for this long.
	primeStaleClaim = 14 * 24 * time.Hour
)

// PrimeOptions carries the per-call inputs of the prime layout.
type PrimeOptions struct {
	// Cwd is the session working directory; when it lies inside the repository it
	// selects `when.paths` rules for full display and orders matching memories first.
	Cwd string
	Now time.Time
	// Focus is the 「当前重点」 section (heading included) rendered right after the
	// commit-policy header. P2 fills it (design §2.4); P1 always leaves it empty.
	Focus string
	// Unbounded disables the segment budgets (PrimeEstimate).
	Unbounded bool
}

// RepoRoot is the repository root of a `<root>/.gofer/tracker` store ("" for an
// explicit tracker directory elsewhere).
func (s *Store) RepoRoot() string {
	dir := filepath.Clean(s.Dir)
	if filepath.Base(dir) != "tracker" || filepath.Base(filepath.Dir(dir)) != ".gofer" {
		return ""
	}
	return filepath.Dir(filepath.Dir(dir))
}

// CwdRel turns an absolute cwd into the slash path relative to the repository
// root; ok=false when it is outside the repository (or the root is unknown).
func (s *Store) CwdRel(cwd string) (string, bool) {
	root := s.RepoRoot()
	if root == "" || cwd == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, cwd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if rel == "." {
		rel = ""
	}
	return filepath.ToSlash(rel), true
}

func (s *Store) defaultPrimeOptions() PrimeOptions {
	cwd, _ := os.Getwd()
	return PrimeOptions{Cwd: cwd, Now: time.Now()}
}

func (s *Store) Prime() (string, error) { return s.PrimeWith(s.defaultPrimeOptions()) }

// PrimeWith renders the local prime: header → [focus] → rules → in-progress →
// ready → memory index → handoffs. Server sections are appended by the caller.
func (s *Store) PrimeWith(opts PrimeOptions) (string, error) {
	out, _, err := s.renderPrime(opts)
	return out, err
}

// PrimeEstimate reports the unbudgeted local context size and whether the budgets
// cut anything. Server-backed scoped memories and handoffs are outside it.
func (s *Store) PrimeEstimate() (bytes int, truncated bool, err error) {
	opts := s.defaultPrimeOptions()
	opts.Unbounded = true
	full, _, err := s.renderPrime(opts)
	if err != nil {
		return 0, false, err
	}
	_, truncated, err = s.renderPrime(s.defaultPrimeOptions())
	return len([]byte(full)), truncated || len([]byte(full)) > PrimeMaxBytes, err
}

func (s *Store) renderPrime(opts PrimeOptions) (string, bool, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	cfg, err := s.ReadConfig()
	if err != nil {
		return "", false, err
	}
	policy, err := CommitPolicyText(cfg.CommitPolicy)
	if err != nil {
		return "", false, err
	}
	var out strings.Builder
	out.WriteString("## 提交策略\n")
	out.WriteString(policy)
	out.WriteString("\n")
	out.WriteString(WorkPrimeHint)
	out.WriteString("\n")
	out.WriteString(TrackerPrimeHint)
	out.WriteString("\n")
	truncated := false
	budget := func(n int) int {
		if opts.Unbounded {
			return -1
		}
		return n
	}
	// P2 hook point: the 「当前重点」 section goes here, before everything else.
	if strings.TrimSpace(opts.Focus) != "" {
		focus := opts.Focus
		if b := budget(primeFocusBudget); b >= 0 && len(focus) > b {
			focus = utf8Prefix(focus, b) + "\n"
			truncated = true
		}
		out.WriteString("\n")
		out.WriteString(focus)
	}
	var memories []Memory
	if cfg.Prime.MemoryEnabled() {
		if memories, err = s.ReadMemories(); err != nil {
			return "", false, err
		}
	}
	rel, inRepo := s.CwdRel(opts.Cwd)
	view := newMemoryView(memories, memoryViewOptions{CwdRel: rel, CwdKnown: inRepo, Now: opts.Now, Stale: s.primeStaleKeys(memories, cfg, opts.Now)})
	if cfg.Prime.MemoryEnabled() {
		seg := view.rulesSegment(budget(primeRulesBudget))
		truncated = seg.write(&out) || truncated
	}
	if cfg.Prime.IssuesEnabled() {
		issues, err := s.ReadIssues()
		if err != nil {
			return "", false, err
		}
		seg := activeSegment(issues, cfg.Prime.ActiveLimit(), opts.Now, budget(primeActiveBudget))
		truncated = seg.write(&out) || truncated
	}
	if cfg.Prime.ReadyEnabled() {
		ready, err := s.Ready()
		if err != nil {
			return "", false, err
		}
		seg := readySegment(ready, cfg.Prime.ReadyCount(), opts.Now, budget(primeReadyBudget))
		truncated = seg.write(&out) || truncated
	}
	if cfg.Prime.MemoryEnabled() {
		seg := view.indexSegment("记忆索引", cfg.Prime.SummaryLimit(), budget(primeIndexBudget))
		truncated = seg.write(&out) || truncated
		seg = view.handoffSegment(budget(primeHandoffBudget))
		truncated = seg.write(&out) || truncated
	}
	body := out.String()
	if !opts.Unbounded && len(body) > PrimeMaxBytes {
		const notice = "\n[内容已截断：使用 gofer issue ls / gofer memory ls 查看全部]\n"
		body = utf8Prefix(body, PrimeMaxBytes-len(notice)) + notice
		truncated = true
	}
	return body, truncated, nil
}

// primeSegment is one budgeted prime section. Lines are kept in order until the
// budget is reached; the rest is summarised by more(n).
type primeSegment struct {
	title  string
	lead   []string // always written (e.g. a fixed hint), counted in the budget
	lines  []string
	budget int // < 0: unbounded
	// extra counts entries omitted before rendering (e.g. by a count limit).
	extra int
	more  func(n int) string
}

// write renders the segment; it reports whether entries were left out.
func (p primeSegment) write(out *strings.Builder) bool {
	if p.title == "" {
		return false
	}
	head := "\n## " + p.title + "\n"
	used := len(head)
	for _, line := range p.lead {
		used += len(line)
	}
	kept := len(p.lines)
	if p.budget >= 0 {
		total := used
		for _, line := range p.lines {
			total += len(line)
		}
		if total > p.budget || p.extra > 0 {
			reserve := 0
			if p.more != nil {
				reserve = len(p.more(len(p.lines) + p.extra))
			}
			kept = 0
			for _, line := range p.lines {
				if used+len(line)+reserve > p.budget {
					break
				}
				used += len(line)
				kept++
			}
		}
	}
	out.WriteString(head)
	for _, line := range p.lead {
		out.WriteString(line)
	}
	for _, line := range p.lines[:kept] {
		out.WriteString(line)
	}
	omitted := len(p.lines) - kept + p.extra
	if omitted > 0 && p.more != nil {
		out.WriteString(p.more(omitted))
	}
	return omitted > 0
}

func issueTouchedAt(item Issue) string {
	for _, at := range []string{item.UpdatedAt, item.StartedAt, item.CreatedAt} {
		if at != "" {
			return at
		}
	}
	return ""
}

// activeSegment lists in_progress issues only (§2.7); open-but-assigned issues
// are shown in ready with their assignee instead.
func activeSegment(issues []Issue, limit int, now time.Time, budget int) primeSegment {
	active := make([]Issue, 0)
	for _, item := range issues {
		if item.Status == "in_progress" {
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
	seg := primeSegment{title: "进行中 issue", budget: budget, more: func(n int) string {
		return fmt.Sprintf("另有 %d 条：`gofer issue ls --status in_progress`\n", n)
	}}
	for i, item := range active {
		if limit >= 0 && i >= limit {
			seg.extra = len(active) - i
			break
		}
		line := fmt.Sprintf("- %s [%s] %s", item.ID, item.Status, item.Title)
		if item.Assignee != "" {
			line += " @" + item.Assignee
		}
		if t, ok := parseTime(issueTouchedAt(item)); ok && now.Sub(t) > primeStaleClaim {
			line += fmt.Sprintf("（认领 %d 天无更新）", ageDays(t, now))
		}
		seg.lines = append(seg.lines, line+"\n")
	}
	return seg
}

func readySegment(ready []Issue, limit int, now time.Time, budget int) primeSegment {
	seg := primeSegment{title: fmt.Sprintf("ready 前 %d", limit), budget: budget, more: func(n int) string {
		return fmt.Sprintf("另有 %d 条：`gofer issue ready`\n", n)
	}}
	for i, item := range ready {
		if i >= limit {
			break
		}
		line := fmt.Sprintf("- %s P%d %s", item.ID, item.Priority, item.Title)
		if item.Assignee != "" {
			line += " @" + item.Assignee
		}
		if age := AgeText(issueTouchedAt(item), now); age != "" {
			line += " · " + age
		}
		seg.lines = append(seg.lines, line+"\n")
	}
	return seg
}
