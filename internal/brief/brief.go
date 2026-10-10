// Package brief assembles the handoff brief (接手包, design
// 2026-10-10-handoff-brief-and-knowledge-loop §一): everything a fresh agent session
// needs to take over an issue or a plan, in one bounded text.
//
// It reads the repository tracker, git and docs/ directly, and the server through the
// narrow Client interface; a section that needs the server is skipped with a note
// when there is none. Like internal/focusremote it sits between the command layer
// and tracker (tracker must not import client), so `gofer issue|plan brief` and the
// MCP gofer_issue_brief / gofer_plan_brief tools only bind inputs and print.
package brief

import (
	"fmt"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/tracker"
)

// DefaultMaxLines caps the rendered brief (`--max-lines`).
const DefaultMaxLines = 400

// Client is the slice of the server API a brief reads.
type Client interface {
	ListJobs(opts job.ListOpts) ([]job.JobResult, error)
	ListPlans(opts client.PlanListOpts) (client.PlanList, error)
	GetPlan(id string) (client.Plan, error)
	GetPlanHandoff(id string, version int) (client.PlanHandoff, error)
	ListScopedMemories(opts client.ScopedMemoryListOpts) ([]client.ScopedMemory, error)
}

// ClientTimeout bounds every server request a brief makes, so an unreachable server
// costs seconds, not the client's default 30s per call. 3s proved too tight while the
// host was busy (a 200-job list timed out under a full test run); a refused
// connection still fails at once, so only a hung server pays the full bound.
const ClientTimeout = 8 * time.Second

// Connect re-binds cli with ClientTimeout and probes the server once. It returns
// (nil, reason) when the server does not answer, so the server sections are skipped
// with that reason instead of each one waiting on its own timeout.
func Connect(cli *client.Client) (Client, string) {
	if cli == nil {
		return nil, "no server configured"
	}
	short := client.NewWithTimeout(cli.BaseURL(), cli.Token(), ClientTimeout)
	if _, err := short.Meta(); err != nil {
		return nil, err.Error()
	}
	return short, ""
}

// Options are the inputs of a brief.
type Options struct {
	// Store is the repository tracker (required for an issue brief; optional for a
	// plan brief, where it resolves the issues the todos mention).
	Store *tracker.Store
	// Root is the repository root for git and docs/ (default Store.RepoRoot()).
	Root string
	// Client is the server; nil skips the server sections with ClientNote.
	Client     Client
	ClientNote string
	// ProjectKey scopes job / plan / project-memory lookups ("" = all projects for
	// jobs and plans, no project memories).
	ProjectKey string
	// AgentName filters `agent:<name>` tagged memories (tracker.MemoryForAgent).
	AgentName string
	MaxLines  int
	Now       time.Time
}

func (o Options) root() string {
	if o.Root != "" {
		return o.Root
	}
	if o.Store != nil {
		return o.Store.RepoRoot()
	}
	return ""
}

func (o Options) maxLines() int {
	if o.MaxLines > 0 {
		return o.MaxLines
	}
	return DefaultMaxLines
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// serverNote is the note of a section skipped for want of a server.
func (o Options) serverNote() string {
	note := "未连接 server，本节跳过"
	if o.ClientNote != "" {
		note += "（" + o.ClientNote + "）"
	}
	return note
}

// Section is one titled part of a brief.
type Section struct {
	Title string   `json:"title"`
	Lines []string `json:"lines,omitempty"`
	// Note says why the section is empty or partial (e.g. no server).
	Note string `json:"note,omitempty"`
	// More is the command that shows what the brief left out.
	More string `json:"more,omitempty"`
	// Truncated counts the lines cut to fit max-lines.
	Truncated int `json:"truncated,omitempty"`
}

// Brief is a rendered handoff brief.
type Brief struct {
	Kind     string    `json:"kind"` // issue | plan
	ID       string    `json:"id"`
	Sections []Section `json:"sections"`
}

func (b Brief) header() string { return fmt.Sprintf("# 接手包 %s %s", b.Kind, b.ID) }

// Text renders the brief as markdown-ish text.
func (b Brief) Text() string {
	var out strings.Builder
	out.WriteString(b.header() + "\n")
	for _, s := range b.Sections {
		out.WriteString("\n## " + s.Title + "\n")
		if s.Note != "" {
			out.WriteString("（" + s.Note + "）\n")
		}
		for _, line := range s.Lines {
			out.WriteString(line + "\n")
		}
		if s.Truncated > 0 {
			out.WriteString(truncatedMarker(s))
		}
	}
	return out.String()
}

func truncatedMarker(s Section) string {
	marker := fmt.Sprintf("[本节截断 %d 行", s.Truncated)
	if s.More != "" {
		marker += "：`" + s.More + "`"
	}
	return marker + "]\n"
}

// sectionCost is the rendered line count of s: blank + heading + note + lines.
func sectionCost(s Section) int {
	n := 2 + len(s.Lines)
	if s.Note != "" {
		n++
	}
	return n
}

// fit cuts sections, in order, so the whole brief stays within maxLines. Every
// section keeps its heading (and note) and the last section (the fixed takeover
// hints) is never cut; an earlier section gets what the sections after it do not
// need for their headings, so the first sections — the issue itself, its tree,
// its design — keep the most.
func fit(sections []Section, maxLines int) []Section {
	total := 1
	for _, s := range sections {
		total += sectionCost(s)
	}
	if total <= maxLines || len(sections) == 0 {
		return sections
	}
	out := append([]Section(nil), sections...)
	left := maxLines - 1 - sectionCost(out[len(out)-1])
	for i := 0; i < len(out)-1; i++ {
		// Headings (+ note + marker) of the sections still to come.
		reserve := 0
		for _, s := range out[i+1 : len(out)-1] {
			reserve += sectionCost(Section{Note: s.Note}) + 1
		}
		cost := sectionCost(out[i])
		if cost <= left-reserve {
			left -= cost
			continue
		}
		head := sectionCost(Section{Note: out[i].Note}) + 1 // + the marker line
		keep := left - reserve - head
		if keep < 0 {
			keep = 0
		}
		if keep < len(out[i].Lines) {
			out[i].Truncated = len(out[i].Lines) - keep
			out[i].Lines = out[i].Lines[:keep]
		}
		left -= head + len(out[i].Lines)
	}
	return out
}

// hintSection is the fixed tail of every brief. claimID is the issue to claim ("" in
// a plan brief, which has no memory section of its own).
func hintSection(store *tracker.Store, claimID string) Section {
	s := Section{Title: "接手提示"}
	if store != nil {
		if cfg, err := store.ReadConfig(); err == nil {
			if policy, err := tracker.CommitPolicyText(cfg.CommitPolicy); err == nil {
				s.Lines = append(s.Lines, "- 提交策略："+policy)
			}
		}
	}
	rules := "- 本地提交 / push 规则与验证命令以「适用记忆」里的 rule 为准（`gofer repo prime` 有全集）。"
	if claimID == "" {
		rules = "- 本地提交 / push 规则与验证命令以 rule 记忆为准（`gofer repo prime`，或关联 issue 的 `gofer issue brief <id>`）。"
	}
	s.Lines = append(s.Lines, rules,
		"- 记忆 / 规则与实际不符：`gofer memory flag <key> --reason \"…\"`，不要静默绕过。")
	if claimID != "" {
		s.Lines = append(s.Lines, fmt.Sprintf("- 开工前 `gofer issue update %s --claim`；进展 `gofer issue comment %s \"…\"`；完成 `gofer issue close %s --reason \"…\"`。", claimID, claimID, claimID))
	}
	return s
}

// indentBlock renders a multi-line text as indented lines.
func indentBlock(text, prefix string) []string {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	parts := strings.Split(text, "\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, prefix+strings.TrimRight(p, " \t\r"))
	}
	return out
}

// firstLine is the first non-empty line of text, capped at limit runes.
func firstLine(text string, limit int) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return capRunes(line, limit)
		}
	}
	return ""
}

func capRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// shortTime renders an RFC3339 tracker stamp as 2006-01-02 15:04.
func shortTime(value string) string {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.Local().Format("2006-01-02 15:04")
		}
	}
	return value
}

func unixDate(sec int64) string {
	if sec <= 0 {
		return ""
	}
	return time.Unix(sec, 0).Local().Format("2006-01-02 15:04")
}
