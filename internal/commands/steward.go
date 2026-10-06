package commands

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/steward"
)

// `gofer steward` (W2b): the steward is a resident ACP session that only schedules and
// tidies your work items. Enable it and pick its agent on the settings page (or
// `PUT /v1/config/steward`); everything it needs lives in the database, so `restart` or an
// agent switch loses nothing.

type stewardOptions struct {
	asJSON        bool
	noWait        bool
	force         bool
	edit, history bool
	version       int
	setFile       string
	timeoutSec    int
}

var stewardOpts stewardOptions

// NewStewardCmd builds the `steward` command group.
func NewStewardCmd() *gcli.Command {
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&stewardOpts.asJSON, "json", "", false, "print JSON")
	}
	return &gcli.Command{
		Name:    "steward",
		Aliases: []string{"stw"},
		Desc:    "The work steward: a resident agent that schedules and tidies your work items (status / start / stop / ask / notes / review)",
		Subs: []*gcli.Command{
			{
				Name: "status", Desc: "Show whether the steward is on, which agent, whether a session is running, and the last review",
				Config: bind,
				Func:   runStewardStatus,
			},
			{
				Name: "start", Desc: "Open the steward session now (it also starts on demand: a question, the daily review)",
				Config: bind,
				Func:   runStewardStart,
			},
			{
				Name: "restart", Desc: "End the session and open a fresh one from the prime (nothing is lost: it lives in the database)",
				Config: bind,
				Func:   runStewardRestart,
			},
			{
				Name: "stop", Desc: "End the steward session (the next need rebuilds it)",
				Config: bind,
				Func:   runStewardStop,
			},
			{
				Name: "ask", Desc: "Put a question to the steward (it starts if needed); prints its reply",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("question", "what to ask, e.g. \"我手上还有什么没完成？\"", true)
					c.BoolOpt(&stewardOpts.noWait, "no-wait", "", false, "do not wait for the reply: print the session job id and return")
					c.IntOpt(&stewardOpts.timeoutSec, "timeout", "", 180, "seconds to wait for the reply")
				},
				Func: runStewardAsk,
			},
			{
				Name: "notes", Desc: "Show the steward's notes (versioned Markdown); --edit opens $EDITOR and saves a new version; --history lists versions",
				Config: func(c *gcli.Command) {
					bind(c)
					c.BoolOpt(&stewardOpts.edit, "edit", "e", false, "edit the notes in $VISUAL/$EDITOR and save a new version")
					c.BoolOpt(&stewardOpts.history, "history", "", false, "list every version (newest first)")
					c.IntOpt(&stewardOpts.version, "version", "v", 0, "show this version instead of the latest")
					c.StrOpt(&stewardOpts.setFile, "set-file", "", "", "replace the notes with this file's content (- = stdin)")
				},
				Func: runStewardNotes,
			},
			{
				Name: "review", Desc: "Run a review now: the steward looks at the work items that changed since the last one (--force: even if none did)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.BoolOpt(&stewardOpts.force, "force", "", false, "review even when nothing changed")
				},
				Func: runStewardReview,
			},
			{
				Name: "merges", Desc: "List the merge suggestions the steward recorded (a person confirms them: `merge-accept` / `merge-dismiss`)",
				Config: bind,
				Func:   runStewardMerges,
			},
			{
				Name: "merge-accept", Desc: "Accept a merge suggestion: performs the merge",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "suggestion id (from `steward merges`)", true)
				},
				Func: func(c *gcli.Command, _ []string) error { return runStewardMergeDecision(c, true) },
			},
			{
				Name: "merge-dismiss", Desc: "Dismiss a merge suggestion",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "suggestion id (from `steward merges`)", true)
				},
				Func: func(c *gcli.Command, _ []string) error { return runStewardMergeDecision(c, false) },
			},
		},
	}
}

func stewardClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

func stewardStateLabel(s string) string {
	switch s {
	case steward.StateRunning:
		return "running（正在处理一轮）"
	case steward.StateIdle:
		return "idle（空闲，随时可问）"
	}
	return "not_started（未启动：有人问或到巡检时间会自动启动）"
}

func runStewardStatus(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	st, err := cli.StewardStatus()
	if err != nil {
		return err
	}
	if stewardOpts.asJSON {
		return workPrintJSON(c, st)
	}
	s := st.Status
	on := "off"
	if s.Enabled {
		on = "on"
	}
	agent := s.Agent
	if agent == "" {
		agent = "(none chosen)"
	}
	c.Printf("steward:   %s\nagent:     %s\nproject:   %s\nsession:   %s\n", on, agent, s.Project, stewardStateLabel(s.State))
	if s.JobID != "" {
		c.Printf("job:       %s (agent %s, turn %d)\n", s.JobID, s.JobAgent, s.TurnNo)
	}
	if s.AgentError != "" {
		c.Printf("agent problem: %s\n", s.AgentError)
	}
	c.Printf("review at: %s daily  | idle end: %d min | event wake: %v | pending events: %d\n", s.ReviewTime, s.IdleEndMin, s.EventWake, s.PendingEvents)
	c.Printf("notes:     v%d, %d bytes", s.NotesVersion, s.NotesBytes)
	if s.NotesNeedSlim {
		c.Print("  (over 8KB: the next review asks the steward to slim them down)")
	}
	c.Println()
	if r := s.LastReview; r != nil {
		c.Printf("last review: %s %s (%s, %d item(s))", r.Day, r.State, r.Trigger, countCSV(r.ItemIDs))
		if r.Error != "" {
			c.Printf(" — %s", r.Error)
		}
		c.Println()
		if r.Summary != "" {
			c.Printf("  点评：%s\n", oneLine(r.Summary, 200))
		}
	}
	if !s.Enabled {
		c.Println("\nThe steward is off. Enable it and pick an acp-agent on the settings page (设置 → 工作 → 管家).")
	}
	return nil
}

func countCSV(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return len(strings.Split(s, ","))
}

func runStewardStart(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	res, err := cli.StewardStart()
	if err != nil {
		return err
	}
	if stewardOpts.asJSON {
		return workPrintJSON(c, res)
	}
	if res.Started {
		c.Printf("steward started: job %s\n", res.JobID)
	} else {
		c.Printf("steward already running: job %s\n", res.JobID)
	}
	return nil
}

func runStewardRestart(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	res, err := cli.StewardRestart()
	if err != nil {
		return err
	}
	if stewardOpts.asJSON {
		return workPrintJSON(c, res)
	}
	c.Printf("steward restarted: job %s\n", res.JobID)
	return nil
}

func runStewardStop(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	stopped, err := cli.StewardStop()
	if err != nil {
		return err
	}
	if stewardOpts.asJSON {
		return workPrintJSON(c, map[string]bool{"stopped": stopped})
	}
	if stopped {
		c.Println("steward session ended")
	} else {
		c.Println("no steward session was running")
	}
	return nil
}

// lastTurnText returns what the session wrote in its latest turn: stdout carries a
// `--- turn N ---` line per turn, so the reply is whatever follows the last one.
func lastTurnText(stdout string) string {
	i := strings.LastIndex(stdout, "--- turn ")
	if i < 0 {
		return strings.TrimSpace(stdout)
	}
	rest := stdout[i:]
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	return strings.TrimSpace(rest)
}

func runStewardAsk(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	q := strings.TrimSpace(c.Arg("question").String())
	before := 0
	if st, serr := cli.StewardStatus(); serr == nil {
		before = st.Status.TurnNo
		if st.Status.State == steward.StateStopped {
			before = 0
		}
	}
	res, err := cli.StewardAsk(q)
	if err != nil {
		return err
	}
	if res.Started {
		before = 0
	}
	if stewardOpts.noWait {
		if stewardOpts.asJSON {
			return workPrintJSON(c, res)
		}
		c.Printf("asked (job %s); the reply streams in the web 「问管家」 panel or `gofer job logs %s`\n", res.JobID, res.JobID)
		return nil
	}
	deadline := time.Now().Add(time.Duration(stewardOpts.timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(700 * time.Millisecond)
		st, serr := cli.StewardStatus()
		if serr != nil {
			continue
		}
		if st.Status.JobID == res.JobID && st.Status.State == steward.StateIdle && st.Status.TurnNo > before {
			out, lerr := cli.GetLogs(res.JobID, "stdout")
			if lerr != nil {
				return lerr
			}
			reply := lastTurnText(out)
			if stewardOpts.asJSON {
				return workPrintJSON(c, map[string]any{"job_id": res.JobID, "started": res.Started, "reply": reply})
			}
			c.Println(reply)
			return nil
		}
	}
	return fmt.Errorf("no reply within %ds (the steward is still working: `gofer job logs %s`)", stewardOpts.timeoutSec, res.JobID)
}

func runStewardNotes(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	if stewardOpts.history {
		h, herr := cli.StewardNotesHistory()
		if herr != nil {
			return herr
		}
		if stewardOpts.asJSON {
			return workPrintJSON(c, h)
		}
		if len(h) == 0 {
			c.Println("no notes yet")
			return nil
		}
		c.Printf("%-8s %-8s %-22s %s\n", "VERSION", "BYTES", "BY", "WHEN")
		for _, n := range h {
			c.Printf("%-8d %-8d %-22s %s\n", n.Version, len(n.Body), oneLine(n.By, 22), ago(n.At))
		}
		return nil
	}
	if stewardOpts.setFile != "" {
		return stewardSetNotesFromFile(c, cli, stewardOpts.setFile)
	}
	cur, err := cli.StewardNotes(stewardOpts.version)
	if err != nil {
		return err
	}
	if stewardOpts.edit {
		return stewardEditNotes(c, cli, cur)
	}
	if stewardOpts.asJSON {
		return workPrintJSON(c, cur)
	}
	if cur.Notes.Version == 0 {
		c.Println("(no notes yet — `gofer steward notes --edit` writes the first version)")
		return nil
	}
	c.Printf("# steward notes v%d (%d bytes, by %s, %s)\n\n%s\n", cur.Notes.Version, len(cur.Notes.Body), cur.Notes.By, ago(cur.Notes.At), cur.Notes.Body)
	return nil
}

func stewardSetNotesFromFile(c *gcli.Command, cli *client.Client, path string) error {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return err
	}
	cur, err := cli.StewardNotes(0)
	if err != nil {
		return err
	}
	return stewardSaveNotes(c, cli, string(data), cur.Notes.Version)
}

func stewardSaveNotes(c *gcli.Command, cli *client.Client, body string, version int) error {
	res, err := cli.PutStewardNotes(body, version)
	var conf *client.ErrStewardNotesConflict
	if errors.As(err, &conf) {
		return fmt.Errorf("the notes changed while you were editing (now v%d, you started from v%d): run `gofer steward notes` and merge your change", conf.Current.Version, version)
	}
	if err != nil {
		return err
	}
	c.Printf("saved steward notes v%d (%d bytes)\n", res.Notes.Version, len(res.Notes.Body))
	if res.Info.NeedSlim {
		c.Println("note: over 8KB — the next review asks the steward to slim them down (old versions are kept)")
	}
	return nil
}

func stewardEditNotes(c *gcli.Command, cli *client.Client, cur client.StewardNotesResp) error {
	ed, tried := resolveEditor()
	if ed == "" {
		return fmt.Errorf("no usable editor found (tried %s); set $EDITOR or use --set-file", strings.Join(tried, ", "))
	}
	dir, err := os.MkdirTemp("", "gofer-steward-notes-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "steward-notes-v"+strconv.Itoa(cur.Notes.Version)+".md")
	if err := os.WriteFile(path, []byte(cur.Notes.Body), 0o600); err != nil {
		return err
	}
	cmd := exec.Command(ed, path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %s failed: %w", ed, err)
	}
	edited, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if string(edited) == cur.Notes.Body {
		c.Println("no changes")
		return nil
	}
	return stewardSaveNotes(c, cli, string(edited), cur.Notes.Version)
}

func runStewardReview(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	res, err := cli.StewardReview(stewardOpts.force)
	if err != nil {
		return err
	}
	if stewardOpts.asJSON {
		return workPrintJSON(c, res)
	}
	if res.Skipped {
		c.Printf("nothing to review: %s (use --force to run it anyway)\n", res.Reason)
		return nil
	}
	c.Printf("review %d started on job %s: %d work item(s), %d event(s) — see `gofer steward status`\n", res.ReviewID, res.JobID, len(res.Items), res.Events)
	return nil
}

func runStewardMerges(c *gcli.Command, _ []string) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	list, err := cli.ListMergeSuggestions()
	if err != nil {
		return err
	}
	if stewardOpts.asJSON {
		return workPrintJSON(c, list)
	}
	if len(list) == 0 {
		c.Println("no pending merge suggestions")
		return nil
	}
	c.Printf("%-5s %-14s %-14s %-6s %s\n", "ID", "SOURCE", "INTO", "AGE", "REASON")
	for _, s := range list {
		c.Printf("%-5d %-14s %-14s %-6s %s\n", s.ID, s.SourceID, s.TargetID, ago(s.At), oneLine(s.Reason, 80))
	}
	return nil
}

func runStewardMergeDecision(c *gcli.Command, accept bool) error {
	cli, err := stewardClient()
	if err != nil {
		return err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(c.Arg("id").String()), 10, 64)
	if err != nil || n <= 0 {
		return fmt.Errorf("suggestion id must be a positive number")
	}
	if accept {
		d, aerr := cli.AcceptMergeSuggestion(n)
		if aerr != nil {
			return aerr
		}
		c.Printf("merged into %s\n", d.ID)
		return nil
	}
	if err := cli.DismissMergeSuggestion(n); err != nil {
		return err
	}
	c.Println("dismissed")
	return nil
}
