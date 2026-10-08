package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
)

// newSessionNudgeCmd builds `session nudge` (N2 §E, SESS-12): a timed reminder sent to a
// terminal session through the same ladder as the web message box. `nudge <sid> --every
// 30m -m "…"` sets one; ls / rm / pause / resume manage them.
func newSessionNudgeCmd() *gcli.Command {
	idArg := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.AddArg("id", "nudge id", true)
	}
	return &gcli.Command{
		Name: "nudge",
		Desc: "Remind a session on a timer: nudge <sid> (--every <dur> | --when-stalled <dur>) -m <text> [--until <time>]; ls / rm / pause / resume",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("sid", "session id (prefix ok when unique)", true)
			c.StrOpt(&sessionNudgeOpts.every, "every", "", "", "send every <dur> (e.g. 30m, min 1m)")
			c.StrOpt(&sessionNudgeOpts.stalled, "when-stalled", "", "", "send when the session made no progress for <dur> (e.g. 20m, min 1m)")
			c.StrOpt(&sessionNudgeOpts.message, "message", "m", "", "the text sent to the session")
			c.StrOpt(&sessionNudgeOpts.until, "until", "", "", "stop at this time (2h, 3d, 2026-10-08 09:30, RFC3339)")
		},
		Func: runSessionNudgeSet,
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    "List nudges (of one session, or all); ended ones with --all",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("sid", "session id (prefix ok); omit for every session", false)
					c.BoolOpt(&sessionNudgeOpts.all, "all", "", false, "include ended nudges")
				},
				Func: runSessionNudgeList,
			},
			{Name: "remove", Aliases: []string{"rm"}, Desc: "Delete a nudge", Config: idArg,
				Func: func(c *gcli.Command, _ []string) error { return runSessionNudgeAct(c, "rm") }},
			{Name: "pause", Desc: "Pause an active nudge", Config: idArg,
				Func: func(c *gcli.Command, _ []string) error { return runSessionNudgeAct(c, "pause") }},
			{Name: "resume", Desc: "Resume a paused nudge (failure count cleared, timers restart now)", Config: idArg,
				Func: func(c *gcli.Command, _ []string) error { return runSessionNudgeAct(c, "resume") }},
		},
	}
}

func runSessionNudgeSet(c *gcli.Command, _ []string) error {
	every, stalled := strings.TrimSpace(sessionNudgeOpts.every), strings.TrimSpace(sessionNudgeOpts.stalled)
	text := strings.TrimSpace(sessionNudgeOpts.message)
	switch {
	case (every == "") == (stalled == ""):
		return fmt.Errorf("give exactly one of --every <dur> or --when-stalled <dur>")
	case text == "":
		return fmt.Errorf("--message/-m is required")
	}
	kind, raw := "every", every
	if stalled != "" {
		kind, raw = "stalled", stalled
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("parse --%s: %w", map[string]string{"every": "every", "stalled": "when-stalled"}[kind], err)
	}
	if d < time.Minute {
		return fmt.Errorf("the interval must be at least 1m")
	}
	var until int64
	if u := strings.TrimSpace(sessionNudgeOpts.until); u != "" {
		if until, err = parseWorkTime(u, time.Now()); err != nil {
			return fmt.Errorf("parse --until: %w", err)
		}
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, c.Arg("sid").String())
	if err != nil {
		return err
	}
	n, err := cli.CreateSessionNudge(sid, kind, int64(d/time.Second), text, until)
	if err != nil {
		return err
	}
	c.Printf("nudge %s set on session %s: %s\n", n.ID, shortSID(sid), nudgeSummary(n))
	return nil
}

func runSessionNudgeList(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid := ""
	if a := c.Arg("sid"); a != nil && strings.TrimSpace(a.String()) != "" {
		if sid, err = resolveSessionID(cli, a.String()); err != nil {
			return err
		}
	}
	list, err := cli.ListSessionNudges(sid, sessionNudgeOpts.all)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("(no nudges)")
		return nil
	}
	for _, n := range list {
		c.Printf("%-14s %-9s %-8s %s  %s\n", n.ID, shortSID(n.SessionID), n.State, nudgeSummary(n), oneLine(n.Text, 60))
	}
	return nil
}

func runSessionNudgeAct(c *gcli.Command, act string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	id := strings.TrimSpace(argID(c))
	switch act {
	case "rm":
		if err := cli.DeleteSessionNudge(id); err != nil {
			return err
		}
		c.Printf("nudge %s removed\n", id)
		return nil
	case "pause":
		n, err := cli.SetSessionNudgeState(id, "paused")
		if err != nil {
			return err
		}
		c.Printf("nudge %s paused\n", n.ID)
	default:
		n, err := cli.SetSessionNudgeState(id, "active")
		if err != nil {
			return err
		}
		c.Printf("nudge %s resumed: %s\n", n.ID, nudgeSummary(n))
	}
	return nil
}

// nudgeSummary is the one-line state of a nudge: its rule, what it did, and why it stopped.
func nudgeSummary(n client.SessionNudge) string {
	rule := "every " + (time.Duration(n.IntervalSec) * time.Second).String()
	if n.Kind == "stalled" {
		rule = "when stalled " + (time.Duration(n.IntervalSec) * time.Second).String()
	}
	out := fmt.Sprintf("%s, sent %d", rule, n.FireCount)
	if n.UntilAt > 0 {
		out += ", until " + time.Unix(n.UntilAt, 0).Format("2006-01-02 15:04")
	}
	if n.FailCount > 0 {
		out += fmt.Sprintf(", %d failed in a row", n.FailCount)
	}
	if n.PauseReason != "" {
		out += " [paused: " + n.PauseReason + "]"
	}
	if n.EndedReason != "" {
		out += " [ended: " + n.EndedReason + "]"
	}
	return out
}
