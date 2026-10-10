package bdmigrate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/inhere/gofer/internal/tracker"
)

// bd writes two kinds of managed blocks into instruction files.
var bdBlockKinds = []struct{ begin, end string }{
	{"<!-- BEGIN BEADS INTEGRATION", "<!-- END BEADS INTEGRATION -->"},
	{"<!-- BEGIN BEADS CODEX SETUP", "<!-- END BEADS CODEX SETUP -->"},
}

// BlockPlan is the planned edit of one instruction file.
type BlockPlan struct {
	File    string `json:"file"`
	Action  string `json:"action"` // none | replace | remove | append | create
	Removed int    `json:"removed_bd_blocks"`
	Gofer   bool   `json:"gofer_block"`
	New     string `json:"-"`
	Old     string `json:"-"`
}

type span struct{ start, end int }

// findBlocks returns the byte spans of every bd block in body, in file order.
// A span covers the BEGIN marker through the END marker and its newline.
func findBlocks(body string) ([]span, error) {
	var spans []span
	for _, kind := range bdBlockKinds {
		from := 0
		for {
			i := strings.Index(body[from:], kind.begin)
			if i < 0 {
				break
			}
			start := from + i
			j := strings.Index(body[start:], kind.end)
			if j < 0 {
				return nil, fmt.Errorf("incomplete block %q (no %q)", kind.begin, kind.end)
			}
			end := start + j + len(kind.end)
			if end < len(body) && body[end] == '\r' {
				end++
			}
			if end < len(body) && body[end] == '\n' {
				end++
			}
			spans = append(spans, span{start, end})
			from = end
		}
	}
	for i := 1; i < len(spans); i++ { // tiny n: insertion sort by start
		for k := i; k > 0 && spans[k].start < spans[k-1].start; k-- {
			spans[k], spans[k-1] = spans[k-1], spans[k]
		}
	}
	return spans, nil
}

// stripSpans returns body without the given spans.
func stripSpans(body string, spans []span) string {
	var b strings.Builder
	last := 0
	for _, sp := range spans {
		b.WriteString(body[last:sp.start])
		last = sp.end
	}
	b.WriteString(body[last:])
	return b.String()
}

// goferSpan locates the gofer block, if any.
func goferSpan(body string) (span, bool) {
	i := strings.Index(body, tracker.BeginBlock)
	if i < 0 {
		return span{}, false
	}
	j := strings.Index(body[i:], tracker.EndBlock)
	if j < 0 {
		return span{}, false
	}
	end := i + j + len(tracker.EndBlock)
	if end < len(body) && body[end] == '\r' {
		end++
	}
	if end < len(body) && body[end] == '\n' {
		end++
	}
	return span{i, end}, true
}

func withEOL(text, sample string) string {
	if strings.Contains(sample, "\r\n") {
		return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
	}
	return text
}

// editInstructionFile computes the new body: the first bd block is replaced in
// place by the gofer block (or dropped when the gofer block already exists or
// skipGofer is set), every further bd block is removed, and everything outside
// the blocks stays byte for byte. A file with no bd block only gains a gofer
// block when it has none and skipGofer is false.
func editInstructionFile(body string, skipGofer bool, goferBlock string) (string, BlockPlan, error) {
	plan := BlockPlan{Old: body}
	spans, err := findBlocks(body)
	if err != nil {
		return body, plan, err
	}
	_, haveGofer := goferSpan(body)
	block := withEOL(goferBlock, body)
	var out string
	switch {
	case len(spans) > 0:
		plan.Removed = len(spans)
		insert := ""
		if !haveGofer && !skipGofer {
			insert = block
			plan.Gofer = true
		}
		var b strings.Builder
		last := 0
		for i, sp := range spans {
			b.WriteString(body[last:sp.start])
			if i == 0 {
				b.WriteString(insert)
			}
			last = sp.end
		}
		b.WriteString(body[last:])
		out = b.String()
		plan.Action = "replace"
		if insert == "" {
			plan.Action = "remove"
		}
	case !haveGofer && !skipGofer:
		out = body
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += withEOL("\n", body)
		}
		out += block
		plan.Action, plan.Gofer = "append", true
	default:
		plan.Action = "none"
		return body, plan, nil
	}
	// Self-check: outside the managed blocks nothing may differ (an appended
	// block only ever adds a missing final newline before itself).
	if plan.Action != "append" && stripGofer(stripSpans(out, mustSpans(out))) != stripGofer(stripSpans(body, spans)) {
		return body, plan, fmt.Errorf("internal check failed: content outside the managed blocks would change")
	}
	plan.New = out
	return out, plan, nil
}

func mustSpans(body string) []span { s, _ := findBlocks(body); return s }

func stripGofer(body string) string {
	if sp, ok := goferSpan(body); ok {
		return body[:sp.start] + body[sp.end:]
	}
	return body
}

// planBlocks plans AGENTS.md / CLAUDE.md. A CLAUDE.md that imports AGENTS.md
// loses its bd block but gets no gofer block (AGENTS.md carries it). With
// neither file present a new AGENTS.md is created.
func planBlocks(root string) ([]BlockPlan, error) {
	var plans []BlockPlan
	importsAgents := tracker.ClaudeImportsAgents(root)
	block := managedBlockFor(root)
	found := false
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		_, plan, err := editInstructionFile(string(b), name == "CLAUDE.md" && importsAgents, block)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		plan.File = name
		plans = append(plans, plan)
	}
	if !found {
		plans = append(plans, BlockPlan{File: "AGENTS.md", Action: "create", Gofer: true, New: block})
	}
	return plans, nil
}

// managedBlockFor is the gofer block for the repository's commit_policy when it
// already has a tracker, else the block for the default policy.
func managedBlockFor(root string) string {
	store := tracker.NewStore(filepath.Join(root, ".gofer", "tracker"))
	if cfg, err := store.ReadConfig(); err == nil {
		if block, err := tracker.ManagedBlockFor(cfg.CommitPolicy); err == nil {
			return block
		}
	}
	return tracker.ManagedBlock()
}
