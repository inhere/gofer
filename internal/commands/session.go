package commands

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

var sessionListOpts = struct {
	project string
	state   string
	agent   string
	all     bool
	limit   int
}{}

var sessionRelayOpts = struct {
	session string
}{}

var sessionSayOpts = struct {
	deliver  bool
	takeover bool
}{}

var sessionWatchOpts struct{ session string }

var sessionNudgeOpts struct {
	every   string
	stalled string
	message string
	until   string
	all     bool
}

var sessionResumeOpts struct {
	input string
	plan  bool
}

// NewSessionCmd builds the `session` command group: the human/CLI face of the
// session relay (SESS-01 §6.2) — list registered terminal agent sessions, flip
// a session's relay switch, answer a waiting turn.
func NewSessionCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "session",
		Aliases: []string{"sess"},
		Desc:    "Terminal agent sessions registered via hooks: list / relay on|off / say (web ↔ terminal message relay)",
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Aliases: []string{"ls"},
				Desc:    "List registered agent sessions (waiting_reply first)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.StrOpt(&sessionListOpts.project, "project", "p", "", "filter by project key")
					c.StrOpt(&sessionListOpts.state, "state", "", "", "filter by state: running|idle|waiting_reply|needs_attention|ended|offline")
					c.StrOpt(&sessionListOpts.agent, "agent", "a", "", "filter by agent: claude|codex|omp|jcode")
					c.BoolOpt(&sessionListOpts.all, "all", "", false, "include ended sessions")
					c.IntOpt(&sessionListOpts.limit, "limit", "", 0, "max rows (default 200)")
				},
				Func: runSessionList,
			},
			{
				Name: "show",
				Desc: "Show one session and its recent relay turns",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "session id (prefix ok when unique)", true)
				},
				Func: runSessionShow,
			},
			{
				Name: "relay",
				Desc: "Set a session's web relay mode auto|on|off (omit --session: the session of the current directory)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("mode", "auto | on | off", true)
					c.StrOpt(&sessionRelayOpts.session, "session", "", "", "session id (default: resolve by current directory)")
				},
				Func: runSessionRelay,
			},
			{
				Name: "say",
				Desc: "Answer a session's waiting turn from the CLI (same as the web input box; --deliver also reaches an idle session in tmux)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "session id (prefix ok when unique)", true)
					c.AddArg("text", "the reply text (`/off` releases the session and turns relay off)", true)
					c.BoolOpt(&sessionSayOpts.deliver, "deliver", "", false,
						"route the reply: answer the OPEN turn if there is one, else type it into the session's tmux pane (§9.1 A)")
					c.BoolOpt(&sessionSayOpts.takeover, "takeover", "", false,
						"with --deliver: if the session has no usable tmux pane, start a new `--resume` process and send it there (§9.1 B; the original terminal stops relaying)")
				},
				Func: runSessionSay,
			},
			{
				Name: "watch",
				Desc: "Register a job for the current agent session's Stop-hook completion notice",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("job-id", "job id to watch", true)
					c.StrOpt(&sessionWatchOpts.session, "session", "", "", "session id (default: resolve by current directory)")
				},
				Func: runSessionWatch,
			},
			{
				Name:    "remove",
				Aliases: []string{"rm"},
				Desc:    "Remove a session registration (its turns stay for audit)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "session id (prefix ok when unique)", true)
				},
				Func: runSessionRemove,
			},
			{
				Name: "resume",
				Desc: "Wake a session up: start a new `--resume` pty process for it (works for ended sessions too); then attach in the web console",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "session id (prefix ok when unique)", true)
					c.StrOpt(&sessionResumeOpts.input, "input", "i", "", "first text typed into the new terminal (optional)")
					c.BoolOpt(&sessionResumeOpts.plan, "plan", "", false, "only show whether and how the session could be woken up; start nothing")
				},
				Func: runSessionResume,
			},
			{
				Name: "release-takeover",
				Desc: "Give a session taken over by a `--resume` pty job back to its original terminal (cancels that job, session returns to idle)",
				Config: func(c *gcli.Command) {
					bindConfigFlag(c)
					bindServerFlags(c)
					c.AddArg("id", "session id (prefix ok when unique)", true)
				},
				Func: runSessionReleaseTakeover,
			},
			newSessionNudgeCmd(),
		},
	}
}

func runSessionWatch(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid := strings.TrimSpace(sessionWatchOpts.session)
	if sid == "" {
		sid, err = resolveCurrentSession(cli)
	} else {
		sid, err = resolveSessionID(cli, sid)
	}
	if err != nil {
		return fmt.Errorf("resolve session for watch: %w", err)
	}
	jobID := strings.TrimSpace(c.Arg("job-id").String())
	if jobID == "" {
		return fmt.Errorf("job id required")
	}
	watch, err := cli.AddSessionJobWatch(sid, jobID)
	if err != nil {
		return err
	}
	c.Printf("watching job %s for session %s: status=%s\n", watch.JobID, shortSID(sid), watch.Status)
	return nil
}

func sessionClient() (*client.Client, error) {
	return newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
}

// resolveSessionID expands a session id prefix against the registered
// sessions (ended included) so users can type the 8 chars the web shows.
func resolveSessionID(cli *client.Client, in string) (string, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", fmt.Errorf("session id required")
	}
	if len(in) >= 32 {
		return in, nil
	}
	list, err := cli.ListSessions(client.SessionListOpts{IncludeEnded: true, Limit: 500})
	if err != nil {
		return "", err
	}
	var hits []string
	for _, a := range list {
		if a.SessionID == in {
			return in, nil
		}
		if strings.HasPrefix(a.SessionID, in) {
			hits = append(hits, a.SessionID)
		}
	}
	switch len(hits) {
	case 0:
		return "", fmt.Errorf("no session matches %q (see `gofer session ls --all`)", in)
	case 1:
		return hits[0], nil
	}
	return "", fmt.Errorf("session prefix %q is ambiguous (%d matches); give more characters", in, len(hits))
}

func runSessionList(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	list, err := cli.ListSessions(client.SessionListOpts{
		Project: sessionListOpts.project, State: sessionListOpts.state, Agent: sessionListOpts.agent,
		IncludeEnded: sessionListOpts.all, Limit: sessionListOpts.limit,
	})
	if err != nil {
		return err
	}
	if len(list) == 0 {
		c.Println("no agent sessions registered (install hooks with `gofer init hooks`)")
		return nil
	}
	c.Print(formatSessionList(list))
	return nil
}

// formatSessionList renders `session ls`. NAME is the agent's own session name
// (Claude Code's `name`, the address other Claude sessions use for SendMessage);
// "-" when the hook has not reported one.
func formatSessionList(list []client.AgentSession) string {
	var b strings.Builder
	// RELAY is 13 wide: `auto·wait(t)` (the longest cell) must not push the rest.
	fmt.Fprintf(&b, "%-9s %-22s %-7s %-15s %-13s %-4s %-5s %-9s %s\n", "SESSION", "NAME", "AGENT", "STATE", "RELAY", "TURN", "SEEN", "PROJECT", "TITLE")
	for _, a := range list {
		name := a.PeerName
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(&b, "%-9s %-22s %-7s %-15s %-13s %-4d %-5s %-9s %s\n",
			shortSID(a.SessionID), name, a.Agent, a.State, relayCell(a), a.TurnNo, ago(a.LastSeenAt), a.ProjectKey, sessionTitle(a))
	}
	return b.String()
}

// relayCell is the RELAY column: the switch the human set (on/off/auto), and —
// for an `auto` session that is waiting right now — the reason it is waiting, so
// "auto" next to a waiting_reply row is explained instead of puzzling.
func relayCell(a client.AgentSession) string {
	if a.RelayMode == "auto" && a.WaitReason != "" {
		switch a.WaitReason {
		case client.WaitIdleProbe:
			return "auto·wait(i)"
		case client.WaitTurnAge:
			return "auto·wait(t)"
		case client.WaitModeOn:
			return "on"
		}
		return "auto·waiting"
	}
	return a.RelayMode
}

// relayDetail is the `session show` relay line: mode, whether it is waiting
// right now, and the evidence the server decided on.
func relayDetail(a client.AgentSession) string {
	switch a.WaitReason {
	case client.WaitModeOn:
		return "on (switch: every stop waits)"
	case client.WaitIdleProbe:
		return fmt.Sprintf("auto: waiting — keyboard idle %ds (>= session.auto_relay_idle_sec)", a.IdleSec)
	case client.WaitTurnAge:
		return fmt.Sprintf("auto: waiting — no human input for %ds (>= session.auto_relay_turn_sec)", humanAgo(a))
	}
	switch a.RelayMode {
	case "on":
		return "on (switch: every stop waits)"
	case "off":
		return "off (this session never waits)"
	}
	// auto, not waiting: show which reading decided that.
	if a.IdleSec >= 0 {
		return fmt.Sprintf("auto: not waiting — keyboard idle %ds (< threshold)", a.IdleSec)
	}
	if a.LastHumanAt > 0 {
		return fmt.Sprintf("auto: not waiting — last human input %ds ago (< threshold; no keyboard probe on this host)", humanAgo(a))
	}
	return "auto: not waiting — no keyboard probe and no human input seen yet"
}

// humanAgo is the seconds since the last human input (0 = never seen).
func humanAgo(a client.AgentSession) int64 {
	if a.LastHumanAt <= 0 {
		return 0
	}
	if d := time.Now().Unix() - a.LastHumanAt; d > 0 {
		return d
	}
	return 0
}

func runSessionShow(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, argID(c))
	if err != nil {
		return err
	}
	d, err := cli.GetSession(sid, 10, "")
	if err != nil {
		return err
	}
	a := d.Session
	if a.PeerName != "" {
		src := ""
		if a.PeerNameSource != "" {
			src = " (" + a.PeerNameSource + ")"
		}
		c.Printf("name:     %s%s\n", a.PeerName, src)
	}
	c.Printf("session:  %s\nagent:    %s\nproject:  %s\nrunner:   %s\ncwd:      %s\ntitle:    %s\nstate:    %s\nrelay:    %s\nmode:     %s\nturns:    %d\nseen:     %s ago\ntranscript: %s\n",
		a.SessionID, a.Agent, a.ProjectKey, a.Runner, a.Cwd, a.Title, a.State, relayDetail(a),
		a.RelayMode, a.TurnNo, ago(a.LastSeenAt), a.Transcript)
	if a.LastMessage != "" {
		c.Printf("\nlast message:\n  %s\n", strings.ReplaceAll(strings.TrimSpace(a.LastMessage), "\n", "\n  "))
	}
	if len(d.Turns) > 0 {
		c.Println("\nrecent turns (newest first):")
		for _, t := range d.Turns {
			ans := t.Answer
			if ans == "" {
				ans = "(" + strings.ToLower(t.State) + ")"
			}
			c.Printf("  [%s] %s\n    agent> %s\n    human> %s\n", t.State, t.Title, oneLine(t.Question, 100), oneLine(ans, 100))
		}
	}
	return nil
}

func runSessionRelay(c *gcli.Command, _ []string) error {
	mode := strings.ToLower(strings.TrimSpace(c.Arg("mode").String()))
	switch mode {
	case "on", "1", "true":
		mode = "on"
	case "off", "0", "false":
		mode = "off"
	case "auto":
	default:
		return errorx.Failf(configExitErr, "relay mode must be auto|on|off, got %q", mode)
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid := strings.TrimSpace(sessionRelayOpts.session)
	if sid == "" {
		sid, err = resolveCurrentSession(cli)
		if err != nil {
			return err
		}
	} else if sid, err = resolveSessionID(cli, sid); err != nil {
		return err
	}
	a, err := cli.SetSessionRelayMode(sid, mode)
	if err != nil {
		return err
	}
	c.Printf("relay %s for session %s (%s)\n", a.RelayMode, shortSID(a.SessionID), sessionTitle(a))
	switch a.RelayMode {
	case "on":
		c.Println("  every stop now waits on the gofer web 会话 page until you reply there (/off releases it)")
	case "auto":
		c.Println("  the server decides: you are away (keyboard idle, or no input for a while) → it waits; your next terminal input releases it")
	default:
		c.Println("  this session never waits; the turn already open (if any) was released")
	}
	return nil
}

// resolveCurrentSession finds the session of the current directory: hub cwd
// match first (exact or ancestor), then the SessionStart marker file.
func resolveCurrentSession(cli *client.Client) (string, error) {
	cwd, _ := os.Getwd()
	list, err := cli.ListSessions(client.SessionListOpts{Cwd: cwd, Limit: 20})
	if err != nil {
		return "", err
	}
	switch len(list) {
	case 1:
		return list[0].SessionID, nil
	case 0:
		if b, rerr := os.ReadFile(currentSessionFile(cwd)); rerr == nil {
			if sid := strings.TrimSpace(string(b)); sid != "" {
				return sid, nil
			}
		}
		return "", fmt.Errorf("no live agent session registered for %s; pass --session <id> (see `gofer session ls`)", cwd)
	}
	var b strings.Builder
	for _, a := range list {
		fmt.Fprintf(&b, "\n  %s  %s  %s  seen %s ago", shortSID(a.SessionID), a.Agent, a.State, ago(a.LastSeenAt))
	}
	return "", fmt.Errorf("%d live sessions match this directory; pass --session <id>:%s", len(list), b.String())
}

func runSessionSay(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, argID(c))
	if err != nil {
		return err
	}
	text := c.Arg("text").String()
	if sessionSayOpts.takeover && !sessionSayOpts.deliver {
		// Answering a waiting turn and taking a session over are different acts; a
		// --takeover that silently did nothing would be worse than saying so.
		return fmt.Errorf("--takeover requires --deliver: a takeover starts a new process, it does not answer a waiting turn")
	}
	if sessionSayOpts.deliver {
		return runSessionDeliver(c, cli, sid, text)
	}
	d, err := cli.SaySession(sid, text)
	if err != nil {
		return err
	}
	c.Printf("answered turn %s (%s)\n", d.ID, d.Title)
	return nil
}

// runSessionDeliver is `session say --deliver`: the routed send of design §9.1.
// An OPEN turn is answered; otherwise the text goes to the session's terminal, so
// a session that is merely idle (no hook waiting) is still reachable. Failures
// come back verbatim: the server's message names the reason code
// (no_runner / no_tmux / ended / no_resume_template / inject_failed:…) and what to
// do about it. With --takeover the server may start a `--resume` pty job instead
// (§9.1 B) and the receipt points at that job, which the web console attaches to.
func runSessionDeliver(c *gcli.Command, cli *client.Client, sid, text string) error {
	res, err := cli.DeliverSession(sid, text, sessionSayOpts.takeover)
	if err != nil {
		return err
	}
	switch res.Path {
	case "tmux":
		c.Printf("typed into the terminal (job %s)\n", res.JobID)
	case "command":
		c.Printf("delivered to the live session process (job %s)\n", res.JobID)
	case "takeover":
		c.Printf("took the session over with a new process (job %s)\n", res.JobID)
	default:
		c.Printf("answered turn %s\n", res.DecisionID)
	}
	return nil
}

func runSessionRemove(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, argID(c))
	if err != nil {
		return err
	}
	if err := cli.DeleteSession(sid); err != nil {
		return err
	}
	c.Printf("session %s removed\n", shortSID(sid))
	return nil
}

// runSessionResume is `session resume <id>`: the explicit wake-up. It starts a new
// interactive `--resume` process for the session — also one whose terminal was
// closed (state ended) — and prints the job to attach to. With --plan it only asks
// the server whether that would work and prints the plain-language answer.
func runSessionResume(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, argID(c))
	if err != nil {
		return err
	}
	if sessionResumeOpts.plan {
		p, err := cli.SessionTakeoverPlan(sid)
		if err != nil {
			return err
		}
		c.Printf("can: %v\nstate: %s\n%s\n", p.Can, p.State, p.Message)
		if p.Reason != "" {
			c.Printf("reason: %s\n", p.Reason)
		}
		if p.Warning != "" {
			c.Printf("warning: %s\n", p.Warning)
		}
		if len(p.Command) > 0 {
			c.Printf("runner: %s  cwd: %s\ncommand: %s\n", p.Runner, p.Cwd, strings.Join(p.Command, " "))
			if p.CwdAbs != "" {
				// The message above already carries the directory and the one-line basis.
				c.Printf("cwd_abs: %s  (source: %s)\n", p.CwdAbs, p.CwdSource)
			}
		}
		return nil
	}
	res, err := cli.ResumeSession(sid, sessionResumeOpts.input)
	if err != nil {
		return err
	}
	c.Printf("woke session %s up with a new process (job %s)\n", shortSID(sid), res.JobID)
	c.Printf("  attach in the web console: /jobs/%s?attach=1; give the session back with `gofer session release-takeover %s`\n", res.JobID, shortSID(sid))
	return nil
}

// runSessionReleaseTakeover is `session release-takeover <id>`: the way back from
// path B (§9.1 B, SUP-02 R1) for the person at the ORIGINAL terminal — the server
// cancels the pty job that holds the session and returns it to idle, so this
// terminal relays again. The server refuses a session that is not taken over
// rather than pretending to have released it, and that message is passed through
// verbatim (a stale request is worth knowing about, not swallowing).
func runSessionReleaseTakeover(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, argID(c))
	if err != nil {
		return err
	}
	a, err := cli.ReleaseSessionTakeover(sid)
	if err != nil {
		return err
	}
	c.Printf("released takeover of session %s (%s): state=%s\n", shortSID(a.SessionID), sessionTitle(a), a.State)
	c.Println("  the terminal that registered this session relays again; start a new takeover by sending a web reply again")
	return nil
}

func shortSID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func sessionTitle(a client.AgentSession) string {
	if a.Title != "" {
		return a.Title
	}
	if a.Cwd != "" {
		return a.Cwd
	}
	return "-"
}

func ago(unix int64) string {
	if unix <= 0 {
		return "-"
	}
	d := time.Since(time.Unix(unix, 0)).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
