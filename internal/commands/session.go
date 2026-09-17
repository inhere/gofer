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
	deliver bool
}{}

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
					c.StrOpt(&sessionListOpts.state, "state", "", "", "filter by state: running|idle|waiting_reply|needs_attention|ended")
					c.StrOpt(&sessionListOpts.agent, "agent", "a", "", "filter by agent: claude|codex")
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
				},
				Func: runSessionSay,
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
		},
	}
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
	// RELAY is 13 wide: `auto·wait(t)` (the longest cell) must not push the rest.
	c.Printf("%-9s %-7s %-15s %-13s %-4s %-5s %-9s %s\n", "SESSION", "AGENT", "STATE", "RELAY", "TURN", "SEEN", "PROJECT", "TITLE")
	for _, a := range list {
		c.Printf("%-9s %-7s %-15s %-13s %-4d %-5s %-9s %s\n",
			shortSID(a.SessionID), a.Agent, a.State, relayCell(a), a.TurnNo, ago(a.LastSeenAt), a.ProjectKey, sessionTitle(a))
	}
	return nil
}

// relayCell is the RELAY column: the switch the human set (on/off/auto), and —
// for an `auto` session that is waiting right now — the reason it is waiting, so
// "auto" next to a waiting_reply row is explained instead of puzzling.
func relayCell(a client.AgentSession) string {
	if a.RelayMode == "auto" && a.Relay {
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
	return modeLabel(a)
}

// modeLabel is the stored switch; a pre-R1 server reports only the derived
// boolean, which reads as `on` when it waits and `auto` otherwise (the old
// server's boolean was exactly the explicit switch plus its own idle rule).
func modeLabel(a client.AgentSession) string {
	if a.RelayMode != "" {
		return a.RelayMode
	}
	if a.Relay {
		return "on"
	}
	return "auto"
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
	d, err := cli.GetSession(sid)
	if err != nil {
		return err
	}
	a := d.Session
	c.Printf("session:  %s\nagent:    %s\nproject:  %s\nrunner:   %s\ncwd:      %s\ntitle:    %s\nstate:    %s\nrelay:    %s\nmode:     %s\nturns:    %d\nseen:     %s ago\ntranscript: %s\n",
		a.SessionID, a.Agent, a.ProjectKey, a.Runner, a.Cwd, a.Title, a.State, relayDetail(a),
		modeLabel(a), a.TurnNo, ago(a.LastSeenAt), a.Transcript)
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
// (no_runner / no_tmux / ended / inject_failed:…) and what to do about it.
func runSessionDeliver(c *gcli.Command, cli *client.Client, sid, text string) error {
	res, err := cli.DeliverSession(sid, text)
	if err != nil {
		return err
	}
	if res.Path == "tmux" {
		c.Printf("typed into the terminal (job %s)\n", res.JobID)
		return nil
	}
	c.Printf("answered turn %s\n", res.DecisionID)
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
