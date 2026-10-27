package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
)

// sessionWaitTimeoutExit is the exit code of `session wait` when the budget ran
// out before the session reached the wanted state. It is deliberately NOT 1: a
// timed-out wait is an outcome a script acts on (go on with something else), not
// a failure, and `gofer job watch` maps terminal states the same way.
const sessionWaitTimeoutExit = 2

// sessionWaitDefaultTimeout is how long `session wait` waits when --timeout is
// not given: long enough for a turn of another agent, short enough that a human
// running it gets an answer instead of a hang.
const sessionWaitDefaultTimeout = 10 * time.Minute

var sessionWaitOpts struct {
	until   string
	timeout string
	jsonOut bool
}

var sessionScreenOpts struct {
	lines   int
	jsonOut bool
}

// newSessionWaitCmd builds `session wait <id>`: block until the session is idle /
// waiting for a reply / gone, so an agent (or a script) can orchestrate other
// sessions instead of polling `session ls` and sleeping.
func newSessionWaitCmd() *gcli.Command {
	return &gcli.Command{
		Name: "wait",
		Desc: "Block until a session reaches a state (idle | waiting_reply | ended). Exit 0 = reached, 2 = timeout, 1 = error",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "session id (prefix ok when unique)", true)
			c.StrOpt(&sessionWaitOpts.until, "until", "", "idle", "state to wait for: idle | waiting_reply | ended (comma-separated for several)")
			c.StrOpt(&sessionWaitOpts.timeout, "timeout", "", "10m", "give up after this long (e.g. 90s, 10m)")
			c.BoolOpt(&sessionWaitOpts.jsonOut, "json", "", false, "print the final session state as JSON")
		},
		Func: runSessionWait,
	}
}

// newSessionScreenCmd builds `session screen <id>`: the tail of what a session
// shows right now (its recent turns, or the pty transcript of a takeover job).
func newSessionScreenCmd() *gcli.Command {
	return &gcli.Command{
		Name: "screen",
		Desc: "Print the tail of what a session shows now (recent relay turns, or a takeover pty job's transcript); read-only",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "session id (prefix ok when unique)", true)
			c.IntOpt(&sessionScreenOpts.lines, "n", "", 50, "how many lines to print (1-500)")
			c.BoolOpt(&sessionScreenOpts.jsonOut, "json", "", false, "print the screen with its source as JSON")
		},
		Func: runSessionScreen,
	}
}

// runSessionWait polls the server until the session reaches one of the wanted
// states or the total --timeout runs out. Each HTTP request is a short long-poll
// (the server caps one request at a minute), and the loop stops as soon as the
// remaining budget is spent — so a total timeout of 10m really is 10m.
func runSessionWait(c *gcli.Command, _ []string) error {
	// The flags are checked BEFORE the client is built: a typo is a usage error and
	// must not need a server (nor say the server could not be reached).
	until, err := parseUntilStates(sessionWaitOpts.until)
	if err != nil {
		return errorx.Failf(configExitErr, "%s", err.Error())
	}
	total := sessionWaitDefaultTimeout
	if raw := strings.TrimSpace(sessionWaitOpts.timeout); raw != "" {
		total, err = time.ParseDuration(raw)
		if err != nil || total <= 0 {
			return errorx.Failf(configExitErr, "--timeout must be a positive duration such as 90s or 10m, got %q", raw)
		}
	}
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, argID(c))
	if err != nil {
		return err
	}
	deadline := time.Now().Add(total)
	var res client.SessionWaitResult
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		res, err = cli.WaitSession(sid, until, remaining)
		if err != nil {
			return fmt.Errorf("wait session %s: %w", shortSID(sid), err)
		}
		if res.Reached || time.Now().After(deadline) {
			break
		}
	}
	if sessionWaitOpts.jsonOut {
		b, jerr := json.MarshalIndent(res, "", "  ")
		if jerr != nil {
			return fmt.Errorf("encode json: %w", jerr)
		}
		c.Println(string(b))
	} else if res.Reached {
		c.Println(cliText("cli.session.wait.reached", "sid", shortSID(res.Session.SessionID), "state", res.State))
	}
	if !res.Reached {
		// Timed out: an outcome, not a failure — but a distinct exit code so a
		// script can branch on it without parsing the text. The message IS the
		// report of the timeout, so nothing else is printed above it.
		return errorx.Failf(sessionWaitTimeoutExit, "session %s did not reach %s within %s (state %s)",
			shortSID(res.Session.SessionID), strings.Join(until, "|"), total, res.State)
	}
	return nil
}

// parseUntilStates reads the --until list and checks it here (before a request is
// sent) so a typo is a usage error rather than a 400 from the server.
func parseUntilStates(raw string) ([]string, error) {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		st := strings.ToLower(strings.TrimSpace(part))
		if st == "" {
			continue
		}
		switch st {
		case "idle", "waiting_reply", "ended":
			out = append(out, st)
		default:
			return nil, fmt.Errorf("--until must be one of idle | waiting_reply | ended (got %q)", st)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--until must be one of idle | waiting_reply | ended")
	}
	return out, nil
}

// runSessionScreen prints the tail of what a session shows: the recent relay turns
// another agent would ask about, or the pty transcript of the job that took the
// session over. Read-only — it never touches the session's terminal.
func runSessionScreen(c *gcli.Command, _ []string) error {
	cli, err := sessionClient()
	if err != nil {
		return err
	}
	sid, err := resolveSessionID(cli, argID(c))
	if err != nil {
		return err
	}
	res, err := cli.SessionScreen(sid, sessionScreenOpts.lines)
	if err != nil {
		return err
	}
	if sessionScreenOpts.jsonOut {
		b, jerr := json.MarshalIndent(res, "", "  ")
		if jerr != nil {
			return fmt.Errorf("encode json: %w", jerr)
		}
		c.Println(string(b))
		return nil
	}
	if strings.TrimSpace(res.Text) == "" {
		c.Println(cliText("cli.session.screen.empty", "sid", shortSID(res.SessionID)))
		return nil
	}
	// The header is machine-ish on purpose (ids, state and source are not prose);
	// the body below it is exactly what the session shows.
	c.Printf("%s · state=%s · source=%s\n\n", shortSID(res.SessionID), res.State, res.Source)
	c.Println(res.Text)
	return nil
}
