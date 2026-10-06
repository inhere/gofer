package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/work"
)

// `gofer work` (W1): the human's work items — one card per thing in flight, above the
// terminal sessions that work on it. `work report` is what a session runs when asked to
// say where it stands.

type workOptions struct {
	asJSON                                bool
	all, unsorted, due                    bool
	status, project, workspace, query     string
	title, goal, blocker, blockerKind     string
	next, summary, priority, rev, session string
	request                               string
	auto, sorted, keep, clear, send, rm   bool
	until, note                           string
	issue, plan, todo, jobID              string
	sessions                              gcli.Strings
	limit                                 int
}

var workOpts workOptions

const workClearValue = "-"

// NewWorkCmd builds the `work` command group.
func NewWorkCmd() *gcli.Command {
	bind := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&workOpts.asJSON, "json", "", false, "print JSON")
	}
	return &gcli.Command{
		Name:    "work",
		Aliases: []string{"wk"},
		Desc:    "Work items: what you are doing across terminal sessions (list / show / set / park / remind / report / merge) — a session reports with `work report`",
		Subs: []*gcli.Command{
			{
				Name: "ls", Aliases: []string{"list"},
				Desc: "List open work items (most recently active first)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.StrOpt(&workOpts.status, "status", "", "", "filter by status (comma-separated): "+strings.Join(jobstore.WorkStatuses, "|"))
					c.StrOpt(&workOpts.project, "project", "p", "", "filter by project key")
					c.StrOpt(&workOpts.workspace, "workspace", "w", "", "filter by workspace directory")
					c.StrOpt(&workOpts.query, "query", "q", "", "text search (title/goal/blocker/next)")
					c.BoolOpt(&workOpts.unsorted, "unsorted", "", false, "only auto-created drafts nobody has sorted yet")
					c.BoolOpt(&workOpts.due, "due", "", false, "only items whose reminder / park deadline has passed")
					c.BoolOpt(&workOpts.all, "all", "", false, "include done / dropped items")
					c.IntOpt(&workOpts.limit, "limit", "", 0, "max rows")
				},
				Func: runWorkList,
			},
			{
				Name: "show", Desc: "Show one work item with its sessions, links and journal",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id (unique prefix ok)", true)
				},
				Func: runWorkShow,
			},
			{
				Name: "new", Desc: "Create a work item",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("title", "title", true)
					workFieldFlags(c, false)
					c.VarOpt(&workOpts.sessions, "session", "", "attach a session id (repeatable)")
				},
				Func: runWorkNew,
			},
			{
				Name: "set", Desc: "Change fields (a status you set here wins over the session-derived one; `--auto` hands it back)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.StrOpt(&workOpts.title, "title", "", "", "new title")
					workFieldFlags(c, true)
					c.BoolOpt(&workOpts.auto, "auto", "", false, "let the session state drive the status again")
					c.BoolOpt(&workOpts.sorted, "sorted", "", false, "mark an auto draft as sorted")
					c.StrOpt(&workOpts.rev, "rev", "", "", "expected rev (optimistic lock; 409 when stale)")
				},
				Func: runWorkSet,
			},
			{
				Name: "note", Desc: "Append a note to the journal",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.AddArg("text", "note text", true)
				},
				Func: runWorkNote,
			},
			{
				Name: "park", Desc: "Park it (status parked) until a time and/or a condition",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.StrOpt(&workOpts.until, "until", "u", "", "wake-up time: 2h | 3d | 2026-10-08 | \"2026-10-08 09:30\" | RFC3339 | tomorrow")
					c.StrOpt(&workOpts.note, "note", "n", "", "condition, e.g. \"到货后继续\"")
				},
				Func: runWorkPark,
			},
			{
				Name: "remind", Desc: "Set (or --clear) the reminder time",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.AddArg("when", "2h | 3d | 2026-10-08 | \"2026-10-08 09:30\" | RFC3339 | tomorrow", false)
					c.BoolOpt(&workOpts.clear, "clear", "", false, "remove the reminder")
				},
				Func: runWorkRemind,
			},
			{
				Name: "report", Desc: "Report where you are (a session runs this when asked): goal / status / blocker / next step / summary",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.StrOpt(&workOpts.goal, "goal", "", "", "what this work is for")
					c.StrOpt(&workOpts.status, "status", "", "", "active|needs_me|waiting_resource|needs_onsite|review|parked (active = the blocker is gone)")
					c.StrOpt(&workOpts.blocker, "blocker", "", "", "what is blocking")
					c.StrOpt(&workOpts.next, "next", "", "", "the next step")
					c.StrOpt(&workOpts.summary, "summary", "", "", "how far it got")
					c.StrOpt(&workOpts.session, "session", "", "", "reporting session id (journal author)")
					c.StrOpt(&workOpts.request, "request", "", "", "the request id you were asked to answer (from the request text; marks it answered)")
				},
				Func: runWorkReport,
			},
			{
				Name: "summarize", Aliases: []string{"tidy"},
				Desc: "Tidy a work item up now: a cheap one-shot model reads the session's transcript tail and fills goal / blocker / next (what a person or the session wrote becomes a suggestion, see `work accept`)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
				},
				Func: runWorkSummarize,
			},
			{
				Name: "accept", Desc: "Adopt a tidy-up suggestion (field: goal | blocker | blocker_kind | next | summary | status_hint)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.AddArg("field", "suggestion field", true)
				},
				Func: runWorkAccept,
			},
			{
				Name: "dismiss", Desc: "Drop a tidy-up suggestion (the same proposal is not made again)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.AddArg("field", "suggestion field", true)
				},
				Func: runWorkDismiss,
			},
			{
				Name: "requests", Aliases: []string{"reqs"},
				Desc: "Show the request ledger (report / hand-over / tidy-up asks and what became of them); with no id, every item",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id (omit for all items)", false)
					c.BoolOpt(&workOpts.all, "all", "", false, "include finished requests (default: only in flight)")
					c.IntOpt(&workOpts.limit, "limit", "", 0, "max rows")
				},
				Func: runWorkRequests,
			},
			{
				Name: "link", Desc: "Link an issue / plan / todo / job / session (--rm removes)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "work item id", true)
					c.StrOpt(&workOpts.issue, "issue", "", "", "tracker issue id")
					c.StrOpt(&workOpts.plan, "plan", "", "", "plan id")
					c.StrOpt(&workOpts.todo, "todo", "", "", "plan todo id")
					c.StrOpt(&workOpts.jobID, "job", "", "", "job id")
					c.StrOpt(&workOpts.session, "session", "", "", "session id (attach it as a current session)")
					c.BoolOpt(&workOpts.rm, "rm", "", false, "remove the link / detach the session")
				},
				Func: runWorkLink,
			},
			{
				Name: "merge", Desc: "Merge other work items into <id> (sessions, links and journals move over)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "target work item id", true)
					c.AddArg("sources", "work item ids to fold in", true, true)
				},
				Func: runWorkMerge,
			},
			{
				Name: "split", Desc: "Split a new work item out of <id> (--session moves sessions; --keep leaves them on both)",
				Config: func(c *gcli.Command) {
					bind(c)
					c.AddArg("id", "source work item id", true)
					c.AddArg("title", "title of the new item", true)
					c.StrOpt(&workOpts.goal, "goal", "", "", "goal of the new item")
					c.VarOpt(&workOpts.sessions, "session", "", "session id to hand over (repeatable)")
					c.BoolOpt(&workOpts.keep, "keep", "", false, "keep the sessions on the source too")
				},
				Func: runWorkSplit,
			},
			{
				Name: "digest", Desc: "Show the daily work digest (deterministic); --send queues it to the webhooks now",
				Config: func(c *gcli.Command) {
					bind(c)
					c.BoolOpt(&workOpts.send, "send", "", false, "send it now as a work.digest notification")
				},
				Func: runWorkDigest,
			},
		},
	}
}

func workFieldFlags(c *gcli.Command, withStatusClear bool) {
	c.StrOpt(&workOpts.goal, "goal", "", "", "goal")
	c.StrOpt(&workOpts.status, "status", "", "", "status: "+strings.Join(jobstore.WorkStatuses, "|"))
	c.StrOpt(&workOpts.blocker, "blocker", "", "", "what is blocking")
	c.StrOpt(&workOpts.blockerKind, "blocker-kind", "", "", "blocker category (device / account / onsite / person …)")
	c.StrOpt(&workOpts.next, "next", "", "", "next step")
	c.StrOpt(&workOpts.summary, "summary", "", "", "summary")
	c.StrOpt(&workOpts.project, "project", "p", "", "project key")
	c.StrOpt(&workOpts.workspace, "workspace", "w", "", "workspace directory")
	c.StrOpt(&workOpts.priority, "priority", "", "", "priority number")
	_ = withStatusClear
}

func workClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

// resolveWorkID accepts a full id or a unique prefix.
func resolveWorkID(cli *client.Client, in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", fmt.Errorf("work item id required")
	}
	if strings.HasPrefix(in, "w-") && len(in) >= 12 {
		return in, nil
	}
	l, err := cli.ListWorkItems(client.WorkListOpts{Closed: true, Limit: 2000})
	if err != nil {
		return "", err
	}
	var hits []string
	for _, w := range l.Items {
		if w.ID == in {
			return in, nil
		}
		if strings.HasPrefix(w.ID, in) {
			hits = append(hits, w.ID)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no work item matches %q (see `gofer work ls --all`)", in)
	case 1:
		return hits[0], nil
	}
	return "", fmt.Errorf("work item prefix %q is ambiguous (%d matches)", in, len(hits))
}

// parseWorkTime turns a human time spec into unix seconds: a duration from now (2h, 30m,
// 3d, 1w), a date or date+time in local time, RFC3339, a unix number, or "tomorrow"
// (09:00 the next day).
func parseWorkTime(s string, now time.Time) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty time")
	}
	if s == "tomorrow" {
		t := now.AddDate(0, 0, 1)
		return time.Date(t.Year(), t.Month(), t.Day(), 9, 0, 0, 0, now.Location()).Unix(), nil
	}
	if n := len(s); n >= 2 {
		unit := s[n-1]
		if v, err := strconv.ParseFloat(s[:n-1], 64); err == nil && v > 0 {
			switch unit {
			case 'd':
				return now.Add(time.Duration(v * 24 * float64(time.Hour))).Unix(), nil
			case 'w':
				return now.Add(time.Duration(v * 7 * 24 * float64(time.Hour))).Unix(), nil
			}
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return now.Add(d).Unix(), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, s, now.Location()); err == nil {
			return t.Unix(), nil
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1_000_000_000 {
		return n, nil
	}
	return 0, fmt.Errorf("cannot parse time %q (use 2h, 3d, 2026-10-08, \"2026-10-08 09:30\", RFC3339 or tomorrow)", s)
}

func workPrintJSON(c *gcli.Command, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	c.Println(string(b))
	return nil
}

func runWorkList(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	o := client.WorkListOpts{Project: workOpts.project, Workspace: workOpts.workspace, Query: workOpts.query,
		Closed: workOpts.all, Due: workOpts.due, Limit: workOpts.limit}
	if workOpts.status != "" {
		o.Statuses = strings.Split(workOpts.status, ",")
	}
	if workOpts.unsorted {
		t := true
		o.Unsorted = &t
	}
	l, err := cli.ListWorkItems(o)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, l)
	}
	if len(l.Items) == 0 {
		c.Println("no work items (they are created automatically from a session's first prompt, or with `gofer work new`)")
		return nil
	}
	c.Printf("%-13s %-16s %-4s %-5s %-6s %s\n", "ID", "STATUS", "SESS", "SEEN", "FLAGS", "TITLE")
	for _, w := range l.Items {
		flags := ""
		if w.Unsorted {
			flags += "U"
		}
		if w.Due {
			flags += "D"
		}
		if w.SessionOffline {
			flags += "O"
		}
		c.Printf("%-13s %-16s %-4d %-5s %-6s %s\n", w.ID, w.Status, len(w.SessionIDs), ago(w.LastActivityAt), flags, w.Title)
	}
	c.Printf("\nneeds_me=%d due=%d open=%d   flags: U=unsorted draft  D=reminder/park due  O=session offline\n",
		l.Summary.NeedsMe, l.Summary.Due, l.Summary.Open)
	return nil
}

func printWorkDetail(c *gcli.Command, d work.DetailView) {
	c.Printf("%s  [%s%s]  rev %d\n", d.ID, d.Status, map[bool]string{true: ", you set it", false: ""}[d.StatusSource == jobstore.WorkSourceHuman], d.Rev)
	c.Printf("title:     %s\n", d.Title)
	srcOf := func(field string) string {
		if fs, ok := d.FieldSources[field]; ok && fs.By != "" {
			return "   (" + fs.By + ", " + ago(fs.At) + " ago)"
		}
		return ""
	}
	for _, kv := range [][3]string{{"goal", d.Goal, srcOf("goal")}, {"blocker", strings.TrimSpace(d.BlockerKind + " " + d.BlockerText), srcOf("blocker")},
		{"next", d.NextStep, srcOf("next")}, {"summary", d.Summary, srcOf("summary")}, {"project", d.ProjectKey, ""}, {"workspace", d.Workspace, ""}, {"park note", d.ParkNote, ""}} {
		if strings.TrimSpace(kv[1]) != "" {
			c.Printf("%-10s %s%s\n", kv[0]+":", kv[1], kv[2])
		}
	}
	if len(d.Suggestions) > 0 {
		c.Println("suggestions (adopt with `gofer work accept <id> <field>`, drop with `work dismiss`):")
		for _, sg := range d.Suggestions {
			c.Printf("  %-12s %s   (%s, %s ago)\n", sg.Field, oneLine(sg.Value, 120), sg.By, ago(sg.At))
		}
	}
	if len(d.Requests) > 0 {
		c.Println("requests:")
		for _, r := range d.Requests {
			note := ""
			if r.Error != "" {
				note = "  " + oneLine(r.Error, 80)
			}
			c.Printf("  %-12s %-9s %-9s %-9s %s ago%s\n", r.ID, r.Kind, r.State, shortSID(r.SessionID), ago(r.CreatedAt), note)
		}
	}
	if d.ParkUntil > 0 {
		c.Printf("park until: %s\n", fmtServerTime(d.ParkUntil))
	}
	if d.RemindAt > 0 {
		c.Printf("remind at:  %s\n", fmtServerTime(d.RemindAt))
	}
	if d.Unsorted {
		c.Println("(unsorted draft — fill in the goal or `work set --sorted`)")
	}
	c.Printf("last activity: %s ago\n", ago(d.LastActivityAt))
	if len(d.Sessions) > 0 {
		c.Println("sessions:")
		for _, s := range d.Sessions {
			state := s.State
			if s.Missing {
				state = "gone"
			}
			c.Printf("  %-8s %-9s %-7s %-14s %s\n", s.Role, shortSID(s.SessionID), s.Agent, state, oneLine(s.Title, 60))
		}
	}
	if len(d.Links) > 0 {
		c.Println("links:")
		for _, l := range d.Links {
			c.Printf("  %-6s %s\n", l.Kind, l.Ref)
		}
	}
	if len(d.Journal) > 0 {
		c.Println("journal:")
		for _, e := range d.Journal {
			origin := ""
			if e.OriginItem != "" {
				origin = " (from " + e.OriginItem + ")"
			}
			c.Printf("  %s  %-7s %s%s: %s\n", time.Unix(e.At, 0).Format("01-02 15:04"), e.Kind, e.By, origin, oneLine(e.Text, 200))
		}
	}
}

func runWorkShow(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	d, err := cli.GetWorkItem(id)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	printWorkDetail(c, d)
	return nil
}

func runWorkNew(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	f := map[string]any{"title": c.Arg("title").String(), "goal": workOpts.goal, "status": workOpts.status,
		"project_key": workOpts.project, "workspace": workOpts.workspace, "next_step": workOpts.next}
	if workOpts.priority != "" {
		n, perr := strconv.Atoi(workOpts.priority)
		if perr != nil {
			return fmt.Errorf("--priority must be a number")
		}
		f["priority"] = n
	}
	if len(workOpts.sessions) > 0 {
		ids := make([]string, 0, len(workOpts.sessions))
		for _, s := range workOpts.sessions {
			sid, rerr := resolveSessionID(cli, s)
			if rerr != nil {
				return rerr
			}
			ids = append(ids, sid)
		}
		f["session_ids"] = ids
	}
	d, err := cli.CreateWorkItem(f)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	c.Printf("created %s  %s\n", d.ID, d.Title)
	return nil
}

func clearable(v string) (any, bool) {
	if v == "" {
		return nil, false
	}
	if v == workClearValue {
		return "", true
	}
	return v, true
}

func runWorkSet(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	f := map[string]any{}
	for key, v := range map[string]string{"title": workOpts.title, "goal": workOpts.goal, "status": workOpts.status,
		"blocker_text": workOpts.blocker, "blocker_kind": workOpts.blockerKind, "next_step": workOpts.next,
		"summary": workOpts.summary, "project_key": workOpts.project, "workspace": workOpts.workspace} {
		if val, ok := clearable(v); ok {
			f[key] = val
		}
	}
	if workOpts.priority != "" {
		n, perr := strconv.Atoi(workOpts.priority)
		if perr != nil {
			return fmt.Errorf("--priority must be a number")
		}
		f["priority"] = n
	}
	if workOpts.auto {
		f["status_source"] = "auto"
	}
	if workOpts.sorted {
		f["unsorted"] = false
	}
	if workOpts.rev != "" {
		n, perr := strconv.ParseInt(workOpts.rev, 10, 64)
		if perr != nil {
			return fmt.Errorf("--rev must be a number")
		}
		f["rev"] = n
	}
	if len(f) == 0 {
		return fmt.Errorf("nothing to change: give at least one field flag")
	}
	d, err := cli.PatchWorkItem(id, f)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	c.Printf("updated %s: status=%s rev=%d\n", d.ID, d.Status, d.Rev)
	return nil
}

func runWorkNote(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	if err := cli.AddWorkNote(id, c.Arg("text").String()); err != nil {
		return err
	}
	c.Printf("noted on %s\n", id)
	return nil
}

func runWorkPark(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	f := map[string]any{"status": jobstore.WorkParked, "park_until": 0}
	if workOpts.until != "" {
		at, perr := parseWorkTime(workOpts.until, time.Now())
		if perr != nil {
			return perr
		}
		f["park_until"] = at
	}
	f["park_note"] = workOpts.note
	if workOpts.until == "" && workOpts.note == "" {
		return fmt.Errorf("park needs --until and/or --note (when should it come back?)")
	}
	d, err := cli.PatchWorkItem(id, f)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	c.Printf("parked %s", d.ID)
	if d.ParkUntil > 0 {
		c.Printf(" until %s", fmtServerTime(d.ParkUntil))
	}
	c.Println()
	return nil
}

func runWorkRemind(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	var at int64
	if !workOpts.clear {
		when := c.Arg("when").String()
		if when == "" {
			return fmt.Errorf("give a time (or --clear)")
		}
		if at, err = parseWorkTime(when, time.Now()); err != nil {
			return err
		}
	}
	d, err := cli.PatchWorkItem(id, map[string]any{"remind_at": at})
	if err != nil {
		return err
	}
	if at == 0 {
		c.Printf("reminder cleared on %s\n", d.ID)
	} else {
		c.Printf("will remind about %s at %s\n", d.ID, fmtServerTime(d.RemindAt))
	}
	return nil
}

func runWorkReport(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	f := map[string]any{"goal": workOpts.goal, "status": workOpts.status, "blocker": workOpts.blocker,
		"next": workOpts.next, "summary": workOpts.summary}
	if rid := strings.TrimSpace(workOpts.request); rid != "" {
		f["request_id"] = rid
	}
	sid := workOpts.session
	if sid == "" {
		sid = os.Getenv("GOFER_SESSION_ID")
	}
	if sid != "" {
		if full, rerr := resolveSessionID(cli, sid); rerr == nil {
			sid = full
		}
		f["session_id"] = sid
	}
	d, err := cli.ReportWork(id, f)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	c.Printf("reported on %s (%s)\n", d.ID, d.Status)
	return nil
}

func runWorkSummarize(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	req, err := cli.SummarizeWork(id)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, req)
	}
	c.Printf("tidy-up started for %s (request %s); see `gofer work show %s` / `gofer work requests %s`\n", id, req.ID, id, id)
	return nil
}

func runWorkAccept(c *gcli.Command, _ []string) error {
	return runWorkSuggestion(c, true)
}

func runWorkDismiss(c *gcli.Command, _ []string) error {
	return runWorkSuggestion(c, false)
}

func runWorkSuggestion(c *gcli.Command, accept bool) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	field := strings.TrimSpace(c.Arg("field").String())
	var d work.DetailView
	verb := "dismissed"
	if accept {
		d, err = cli.AcceptWorkSuggestion(id, field)
		verb = "adopted"
	} else {
		d, err = cli.DismissWorkSuggestion(id, field)
	}
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	c.Printf("%s the %s suggestion on %s\n", verb, field, d.ID)
	return nil
}

func runWorkRequests(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id := strings.TrimSpace(c.Arg("id").String())
	if id != "" {
		if id, err = resolveWorkID(cli, id); err != nil {
			return err
		}
	}
	reqs, err := cli.ListWorkRequests(id, !workOpts.all, workOpts.limit)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, reqs)
	}
	if len(reqs) == 0 {
		c.Println("no requests")
		return nil
	}
	c.Printf("%-12s %-13s %-9s %-9s %-9s %-6s %s\n", "ID", "ITEM", "KIND", "STATE", "SESSION", "AGE", "BY / NOTE")
	for _, r := range reqs {
		note := r.By
		if r.Error != "" {
			note += "  " + oneLine(r.Error, 80)
		}
		c.Printf("%-12s %-13s %-9s %-9s %-9s %-6s %s\n", r.ID, r.WorkItemID, r.Kind, r.State, shortSID(r.SessionID), ago(r.CreatedAt), note)
	}
	return nil
}

func runWorkLink(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	type pair struct{ kind, ref string }
	var pairs []pair
	for _, p := range []pair{{"issue", workOpts.issue}, {"plan", workOpts.plan}, {"todo", workOpts.todo}, {"job", workOpts.jobID}} {
		if p.ref != "" {
			pairs = append(pairs, p)
		}
	}
	if len(pairs) == 0 && workOpts.session == "" {
		return fmt.Errorf("give one of --issue --plan --todo --job --session")
	}
	var d work.DetailView
	for _, p := range pairs {
		if workOpts.rm {
			d, err = cli.UnlinkWork(id, p.kind, p.ref)
		} else {
			d, err = cli.LinkWork(id, p.kind, p.ref)
		}
		if err != nil {
			return err
		}
		c.Printf("%s %s %s\n", map[bool]string{true: "unlinked", false: "linked"}[workOpts.rm], p.kind, p.ref)
	}
	if workOpts.session != "" {
		sid, rerr := resolveSessionID(cli, workOpts.session)
		if rerr != nil {
			return rerr
		}
		if workOpts.rm {
			d, err = cli.DetachWorkSession(id, sid)
		} else {
			d, err = cli.AttachWorkSession(id, sid)
		}
		if err != nil {
			return err
		}
		c.Printf("%s session %s\n", map[bool]string{true: "detached", false: "attached"}[workOpts.rm], shortSID(sid))
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	return nil
}

func runWorkMerge(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	target, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	var srcs []string
	for _, s := range c.Arg("sources").Strings() {
		id, rerr := resolveWorkID(cli, s)
		if rerr != nil {
			return rerr
		}
		srcs = append(srcs, id)
	}
	d, err := cli.MergeWorkItems(target, srcs)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, d)
	}
	c.Printf("merged %d item(s) into %s (%d session(s))\n", len(srcs), d.ID, len(d.SessionIDs))
	return nil
}

func runWorkSplit(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	id, err := resolveWorkID(cli, c.Arg("id").String())
	if err != nil {
		return err
	}
	var sids []string
	for _, s := range workOpts.sessions {
		sid, rerr := resolveSessionID(cli, s)
		if rerr != nil {
			return rerr
		}
		sids = append(sids, sid)
	}
	res, err := cli.SplitWorkItem(id, c.Arg("title").String(), workOpts.goal, sids, workOpts.keep)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, res)
	}
	c.Printf("split %s out of %s\n", res.Item.ID, res.Source.ID)
	return nil
}

func runWorkDigest(c *gcli.Command, _ []string) error {
	cli, err := workClient()
	if err != nil {
		return err
	}
	d, queued, err := cli.WorkDigest(workOpts.send)
	if err != nil {
		return err
	}
	if workOpts.asJSON {
		return workPrintJSON(c, map[string]any{"digest": d, "queued": queued})
	}
	c.Println(d.Title)
	c.Println()
	c.Println(d.Text)
	if workOpts.send {
		c.Printf("\nqueued to %d webhook(s)\n", queued)
	}
	return nil
}
