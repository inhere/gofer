package tracker

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// 「当前重点」 section of prime (design §2.4). Everything is fetched fresh and best
// effort: a part that fails or runs out of time is simply left out, never an error.
const (
	// FocusBudget is the byte budget of the whole section (heading included).
	FocusBudget = primeFocusBudget
	// focusActiveWindow: an in_progress issue untouched for longer is not 「在做」.
	focusActiveWindow = 14 * 24 * time.Hour
	// focusUnlockWindow: issues closed within it may have unlocked ready issues.
	focusUnlockWindow = 3 * 24 * time.Hour
	focusActiveLimit  = 3
	focusPlanLimit    = 2
	focusUnlockLimit  = 3
	focusTitleRunes   = 28
)

// focus line categories, in trim order: env is dropped first, doing last.
const (
	focusDoing = iota
	focusUnlocked
	focusWrapUp
	focusEnv
)

// FocusPlan is an open plan of this project with its progress and next todo.
type FocusPlan struct {
	ID       string
	Title    string
	Done     int // done + skipped todos
	Total    int
	NextTodo string // title of the first unfinished todo ("" = unknown / none)
}

// FocusWorker is one worker runner as the server sees it.
type FocusWorker struct {
	Name    string
	Version string
	Online  bool
}

// FocusServer is the server version and the workers that serve this project
// (the caller leaves out every other worker).
type FocusServer struct {
	Version string
	Workers []FocusWorker
	// UTCOffsetSec is the server's UTC offset (nil = unknown): the section header
	// shows the time in the operator's zone, not the container's.
	UTCOffsetSec *int
}

// FocusRemote is what the command layer fetched from the server; either part may
// be nil / empty when unavailable.
type FocusRemote struct {
	Server *FocusServer
	Plans  []FocusPlan
}

// FocusGit is the repository state; HasTag / HasUpstream gate their parts.
type FocusGit struct {
	Branch         string
	Head           string
	Changed        int  // tracked files with staged or unstaged changes
	ChangedKnown   bool // false when the full worktree scan did not finish in time
	TrackerChanged int  // files under the tracker directory with changes
	HasUpstream    bool
	Ahead          int
	HasTag         bool
	Tag            string
	SinceTag       int
}

// FocusSources are the slow inputs of BuildFocus. Nil members are skipped.
type FocusSources struct {
	Git    GitRunner
	Remote func(ctx context.Context) (FocusRemote, error)
}

// FocusInput is everything RenderFocus needs; nil / empty parts are omitted.
type FocusInput struct {
	Now      time.Time
	Issues   []Issue
	Memories []Memory
	Plans    []FocusPlan
	Git      *FocusGit
	Server   *FocusServer
	Budget   int // 0 = FocusBudget
	// NoEnv drops the environment lines (仓库 / 服务), prime.focus_env=false.
	NoEnv bool
	// ShowTag adds the latest tag to the repository line, prime.focus_tag=true.
	ShowTag bool
}

// BuildFocus collects the focus inputs within ctx (git and server in parallel)
// and renders the section; "" when there is nothing to show. Local tracker read
// errors and slow / failing sources only drop their own lines.
func (s *Store) BuildFocus(ctx context.Context, now time.Time, src FocusSources) string {
	in := FocusInput{Now: now}
	if cfg, err := s.ReadConfig(); err == nil {
		in.NoEnv, in.ShowTag = !cfg.Prime.FocusEnvEnabled(), cfg.Prime.FocusTagEnabled()
	}
	type gitResult struct{ g *FocusGit }
	gitCh := make(chan gitResult, 1)
	remoteCh := make(chan FocusRemote, 1)
	if src.Git != nil {
		go func() {
			g, err := CollectFocusGit(ctx, src.Git, s.RepoRoot(), s.Dir)
			if err != nil {
				g = nil
			}
			gitCh <- gitResult{g}
		}()
	} else {
		gitCh <- gitResult{}
	}
	if src.Remote != nil {
		go func() {
			r, _ := src.Remote(ctx)
			remoteCh <- r
		}()
	} else {
		remoteCh <- FocusRemote{}
	}
	if issues, err := s.ReadIssues(); err == nil {
		in.Issues = issues
	}
	if memories, err := s.ReadMemories(); err == nil {
		in.Memories = memories
	}
	for pending := 2; pending > 0; pending-- {
		select {
		case r := <-gitCh:
			in.Git = r.g
		case r := <-remoteCh:
			in.Server, in.Plans = r.Server, r.Plans
		case <-ctx.Done():
			pending = 0
		}
	}
	return RenderFocus(in)
}

type focusLine struct {
	cat  int
	text string
}

// RenderFocus renders the 「当前重点」 section within the budget, dropping whole
// lines in the order env → 未收尾 → 刚解锁 → 在做 (last line of a category first).
func RenderFocus(in FocusInput) string {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	budget := in.Budget
	if budget <= 0 {
		budget = FocusBudget
	}
	var lines []focusLine
	add := func(cat int, format string, args ...any) {
		lines = append(lines, focusLine{cat: cat, text: "- " + fmt.Sprintf(format, args...) + "\n"})
	}
	for _, it := range focusActiveIssues(in.Issues, in.Now) {
		add(focusDoing, "在做 %s %s", it.ID, truncateRunes(it.Title, focusTitleRunes))
	}
	for i, p := range in.Plans {
		if i >= focusPlanLimit {
			break
		}
		text := fmt.Sprintf("plan %s「%s」%d/%d", p.ID, truncateRunes(p.Title, 20), p.Done, p.Total)
		if p.NextTodo != "" {
			text += "，下一步：" + truncateRunes(p.NextTodo, focusTitleRunes)
		}
		add(focusDoing, "%s", text)
	}
	if h, ok := focusHandoff(in.Memories, in.Now); ok {
		add(focusDoing, "交接 %s：%s", h.Key, truncateRunes(DisplayMemorySummary(h.MemoryMeta, h.Content), 40))
	}
	for _, u := range unlockedIssues(in.Issues, in.Now) {
		add(focusUnlocked, "刚解锁 %s %s（%s 已关闭）", u.issue.ID, truncateRunes(u.issue.Title, focusTitleRunes), u.by)
	}
	if g := in.Git; g != nil {
		var parts []string
		switch {
		case g.ChangedKnown && g.Changed > 0:
			part := fmt.Sprintf("工作树 %d 个文件未提交", g.Changed)
			if g.TrackerChanged > 0 {
				part += fmt.Sprintf("（含 tracker %d）", g.TrackerChanged)
			}
			parts = append(parts, part)
		case !g.ChangedKnown && g.TrackerChanged > 0:
			parts = append(parts, fmt.Sprintf("tracker %d 个文件未提交", g.TrackerChanged))
		}
		if g.HasUpstream && g.Ahead > 0 {
			parts = append(parts, fmt.Sprintf("领先上游 %d 个提交", g.Ahead))
		}
		if len(parts) > 0 {
			add(focusWrapUp, "未收尾：%s", strings.Join(parts, "；"))
		}
		if g.Head != "" && !in.NoEnv {
			text := "仓库：" + strings.TrimSpace(g.Branch+" "+g.Head)
			if g.HasTag && in.ShowTag {
				text += fmt.Sprintf("，最近 tag %s（之后 %d 个提交）", g.Tag, g.SinceTag)
			}
			add(focusEnv, "%s", text)
		}
	}
	if line := focusServerLine(in.Server); line != "" && !in.NoEnv {
		add(focusEnv, "%s", line)
	}
	if len(lines) == 0 {
		return ""
	}
	head := fmt.Sprintf("## 当前重点（自动，%s）\n", focusClock(in.Now, in.Server))
	size := func() int {
		n := len(head)
		for _, l := range lines {
			n += len(l.text)
		}
		return n
	}
	for len(lines) > 0 && size() > budget {
		drop := -1
		for i, l := range lines {
			if drop < 0 || l.cat >= lines[drop].cat {
				drop = i
			}
		}
		lines = append(lines[:drop], lines[drop+1:]...)
	}
	if len(lines) == 0 {
		return ""
	}
	var out strings.Builder
	out.WriteString(head)
	for _, l := range lines {
		out.WriteString(l.text)
	}
	return out.String()
}

// focusActiveIssues: in_progress issues touched within the window, newest first.
func focusActiveIssues(issues []Issue, now time.Time) []Issue {
	var out []Issue
	for _, it := range issues {
		if it.Status != "in_progress" {
			continue
		}
		if t, ok := parseTime(issueTouchedAt(it)); !ok || now.Sub(t) > focusActiveWindow {
			continue
		}
		out = append(out, it)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := issueTouchedAt(out[i]), issueTouchedAt(out[j]); a != b {
			return a > b
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > focusActiveLimit {
		out = out[:focusActiveLimit]
	}
	return out
}

func focusHandoff(memories []Memory, now time.Time) (Memory, bool) {
	live := newMemoryView(memories, memoryViewOptions{Now: now}).liveHandoffs()
	if len(live) == 0 {
		return Memory{}, false
	}
	return live[0], true
}

type unlockedIssue struct {
	issue Issue
	by    string
}

// unlockedIssues lists ready issues that a recently closed blocker released
// (bd `close --suggest-next`): open, no open blocker left, and at least one
// `blocks` dep closed within the window.
func unlockedIssues(issues []Issue, now time.Time) []unlockedIssue {
	status := make(map[string]string, len(issues))
	recent := map[string]bool{}
	for _, it := range issues {
		status[it.ID] = it.Status
		if it.Status != "closed" {
			continue
		}
		at := it.ClosedAt
		if at == "" {
			at = it.UpdatedAt
		}
		if t, ok := parseTime(at); ok && now.Sub(t) <= focusUnlockWindow {
			recent[it.ID] = true
		}
	}
	if len(recent) == 0 {
		return nil
	}
	var out []unlockedIssue
	for _, it := range issues {
		if it.Status != "open" {
			continue
		}
		by, blocked := "", false
		for _, dep := range it.Deps {
			if dep.Type != "blocks" {
				continue
			}
			if status[dep.ID] != "closed" {
				blocked = true
				break
			}
			if by == "" && recent[dep.ID] {
				by = dep.ID
			}
		}
		if !blocked && by != "" {
			out = append(out, unlockedIssue{issue: it, by: by})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].issue.Priority != out[j].issue.Priority {
			return out[i].issue.Priority < out[j].issue.Priority
		}
		return out[i].issue.ID < out[j].issue.ID
	})
	if len(out) > focusUnlockLimit {
		out = out[:focusUnlockLimit]
	}
	return out
}

// focusServerLine: server version, then this project's workers — how many are
// online, how many run a version other than the server's (a count, no names) and
// the offline ones by name. Workers of other projects are never in srv.Workers.
func focusServerLine(srv *FocusServer) string {
	if srv == nil {
		return ""
	}
	version := shortVersion(srv.Version)
	text := "服务："
	if version != "" {
		text += "server " + version
	} else {
		text += "server 在线"
	}
	if len(srv.Workers) == 0 {
		return text
	}
	online, differ, offline := 0, 0, []string(nil)
	for _, w := range srv.Workers {
		if !w.Online {
			offline = append(offline, w.Name)
			continue
		}
		online++
		if shortVersion(w.Version) != version {
			differ++
		}
	}
	text += fmt.Sprintf("；本项目 worker %d/%d 在线", online, len(srv.Workers))
	switch {
	case online > 0 && differ == 0:
		text += "（同版本）"
	case differ > 0:
		text += fmt.Sprintf("，%d 个与 server 版本不同", differ)
	}
	if len(offline) > 0 {
		text += "；离线 " + joinLimited(offline, 3)
	}
	return text
}

// shortVersion drops the build suffix: "0.128.2 (6a52776)" → "0.128.2".
func shortVersion(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ' '); i > 0 {
		v = v[:i]
	}
	return v
}

func joinLimited(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, " · ")
	}
	return strings.Join(items[:limit], " · ") + fmt.Sprintf(" 等 %d 个", len(items))
}

// focusClock formats now in the server's zone when known; otherwise in the local
// zone with its abbreviation, so a container clock is never mistaken for the
// operator's.
func focusClock(now time.Time, srv *FocusServer) string {
	if srv != nil && srv.UTCOffsetSec != nil {
		return now.In(time.FixedZone("", *srv.UTCOffsetSec)).Format("2006-01-02 15:04")
	}
	return now.Local().Format("2006-01-02 15:04 MST")
}
