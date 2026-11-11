package commands

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/streaming"
)

// jobTranscriptOpts holds `job transcript` flags (gofer-mmh0). At most one of
// outline / turns / head / tail / search; the server validates the combination.
var jobTranscriptOpts = struct {
	outline bool
	turns   string
	head    int
	tail    int
	search  string
	cursor  string
	out     string
}{}

func configJobTranscript(c *gcli.Command) {
	bindConfigFlag(c)
	bindServerFlags(c)
	c.BoolOpt(&jobTranscriptOpts.outline, "outline", "", false, "one line per turn with a preview (the default)")
	c.StrOpt(&jobTranscriptOpts.turns, "turns", "", "", "full text of turns A-B (or one turn A)")
	c.IntOpt(&jobTranscriptOpts.head, "head", "", 0, "full text of the first N turns")
	c.IntOpt(&jobTranscriptOpts.tail, "tail", "", 0, "full text of the last N turns")
	c.StrOpt(&jobTranscriptOpts.search, "search", "", "", "find turns containing this literal text (at most 200 characters)")
	c.StrOpt(&jobTranscriptOpts.cursor, "cursor", "", "", "continue a truncated page: the <turn:offset> it printed")
	c.StrOpt(&jobTranscriptOpts.out, "out", "", "", "write the whole result (all pages) to this local file instead of printing one page")
	c.AddArg("id", "job id", true)
}

// transcriptQueryFromOpts maps the flags onto the shared query; a zero head / tail is
// "not given" (a negative one is passed through so the server rejects it).
func transcriptQueryFromOpts() streaming.TranscriptQuery {
	q := streaming.TranscriptQuery{
		Outline: jobTranscriptOpts.outline, Turns: jobTranscriptOpts.turns,
		Search: jobTranscriptOpts.search, Cursor: jobTranscriptOpts.cursor,
	}
	if jobTranscriptOpts.head != 0 {
		q.Head = fmt.Sprint(jobTranscriptOpts.head)
	}
	if jobTranscriptOpts.tail != 0 {
		q.Tail = fmt.Sprint(jobTranscriptOpts.tail)
	}
	return q
}

func runJobTranscript(c *gcli.Command, _ []string) error {
	id := argID(c)
	if id == "" {
		return fmt.Errorf("job transcript requires an <id> argument")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return err
	}
	q := transcriptQueryFromOpts()
	if jobTranscriptOpts.out == "" {
		page, err := cli.GetJobTranscript(id, q)
		if err != nil {
			return err
		}
		var sb strings.Builder
		renderTranscriptPage(&sb, page, true)
		if page.Next != nil {
			fmt.Fprintf(&sb, "\n-- more: --cursor %s\n", page.Next)
		}
		c.Print(sb.String())
		return nil
	}

	// --out: follow the cursor to the end and write everything to a local file (the
	// server never writes files). The file is created only after the first page came back.
	var (
		f  *os.File
		bw *bufio.Writer
	)
	first, pages := true, 0
	for {
		page, err := cli.GetJobTranscript(id, q)
		if err != nil {
			if f != nil {
				_ = bw.Flush()
				_ = f.Close()
			}
			return err
		}
		if f == nil {
			if f, err = os.Create(jobTranscriptOpts.out); err != nil {
				return fmt.Errorf("create %s: %w", jobTranscriptOpts.out, err)
			}
			bw = bufio.NewWriter(f)
		}
		renderTranscriptPage(bw, page, first)
		first = false
		pages++
		if page.Next == nil {
			break
		}
		if q.Cursor == page.Next.String() { // no progress: refuse to loop forever
			_ = bw.Flush()
			_ = f.Close()
			return fmt.Errorf("%s", cliText("cli.job.transcript.no_cursor"))
		}
		q.Cursor = page.Next.String()
	}
	if err := bw.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("write %s: %w", jobTranscriptOpts.out, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", jobTranscriptOpts.out, err)
	}
	c.Printf("wrote %s (%d page(s))\n", jobTranscriptOpts.out, pages)
	return nil
}

// renderTranscriptPage prints a page as plain text. header says whether to print the
// summary line (only the first page of an --out export has it).
func renderTranscriptPage(w io.Writer, page streaming.JobTranscriptPage, header bool) {
	if header {
		fmt.Fprintf(w, "source: %s  mode: %s  turns: %d\n", orDash(page.Source), page.Mode, page.Total)
	}
	if len(page.Turns) == 0 && header {
		fmt.Fprintln(w, "(no turns recorded)")
	}
	for _, t := range page.Turns {
		switch page.Mode {
		case streaming.TranscriptOutline:
			fmt.Fprintf(w, "#%d [%s] %d chars%s%s%s\n", t.Turn, orDash(t.Role), t.Chars, toolSummary(t.Tools), tokenSummary(t.Tokens), errSummary(t.Error))
			if t.Prompt != "" {
				fmt.Fprintf(w, "    prompt: %s\n", t.Prompt)
			}
			if t.Preview != "" {
				fmt.Fprintf(w, "    %s\n", t.Preview)
			}
		case streaming.TranscriptSearch:
			fmt.Fprintf(w, "#%d [%s] %d match(es)\n", t.Turn, orDash(t.Role), t.MatchCount)
			for _, m := range t.Matches {
				fmt.Fprintf(w, "    %s\n", m)
			}
		default:
			if t.Offset == 0 {
				fmt.Fprintf(w, "\n=== turn %d [%s]%s%s%s ===\n", t.Turn, orDash(t.Role), toolSummary(t.Tools), tokenSummary(t.Tokens), errSummary(t.Error))
				if t.Prompt != "" {
					fmt.Fprintf(w, "> %s\n\n", strings.ReplaceAll(t.Prompt, "\n", "\n> "))
				}
			}
			io.WriteString(w, t.Text)
			if !strings.HasSuffix(t.Text, "\n") && t.Offset+len(t.Text) >= t.Chars {
				io.WriteString(w, "\n")
			}
		}
	}
}

func toolSummary(tools map[string]int) string {
	if len(tools) == 0 {
		return ""
	}
	names := make([]string, 0, len(tools))
	for n := range tools {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", n, tools[n]))
	}
	return ", tools: " + strings.Join(parts, " ")
}

func tokenSummary(n int64) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(", %d tokens", n)
}

func errSummary(e string) string {
	if e == "" {
		return ""
	}
	return ", error: " + e
}

// turnHint is the one-line pointer `job show` / `job logs` print for a multi-turn job.
func turnHint(res job.JobResult) string {
	if res.TurnCount <= 1 {
		return ""
	}
	return cliText("cli.job.transcript.turn_hint", "count", res.TurnCount, "id", res.ID)
}

// printTurnHintForLogs fetches the job (best-effort) and prints the hint on stderr, so
// a script reading the log from stdout is unaffected.
func printTurnHintForLogs(cli interface {
	GetJob(string) (job.JobResult, error)
}, id string) {
	res, err := cli.GetJob(id)
	if err != nil {
		return
	}
	if hint := turnHint(res); hint != "" {
		fmt.Fprintln(os.Stderr, hint)
	}
}
