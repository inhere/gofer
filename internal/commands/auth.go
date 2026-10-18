package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// auth.go implements `gofer auth`: what THIS credential holds (`whoami`), what the running server
// asks of every route (`routes`) and how a session grant gets more time (`grant renew`). It is a
// window into the permission model (design「接口」一节, plan T2.1/T3) — grants are created and
// revoked on the web console, and the CLI's job is to answer "why was I refused?" and "this grant
// expires in ten minutes, give it a few more hours" without a browser:
//
//	gofer auth whoami             — the caller, whether it is a root administrator, every permission it
//	                                holds and where each one came from
//	gofer auth routes             — the route table of the running binary, i.e. what each route needs
//	gofer auth grant renew <id>   — push back the deadline of a session grant that is still in force
//
// `gofer auth whoami` is the command a 403 points at (the server's own wording: 「请让根管理员授予
// <perm>（`gofer auth whoami` 查看当前权限）」), so its output has to name the missing permission's
// neighbours, the sources, and — when the deployment has no root administrator at all — say that
// nobody can grant anything.
const authExitErr = 1

// authFail is the coded failure of an `auth` command (gcli derives a non-zero exit only from coded
// errors, like toolFail / configExitErr).
func authFail(format string, a ...any) error {
	return errorx.Failf(authExitErr, format, a...)
}

var authOpts struct {
	asJSON bool
}

// authGrantOpts holds the flags of `gofer auth grant`. It is a package-level struct because gcli
// binds flags straight into it, so it is registered in flagGlobals (G053).
var authGrantOpts struct {
	// renewFor is --for <dur>: how much longer the renewed grant should last, counted from now.
	renewFor time.Duration
}

// authGrantRenewMaxSec is the server's ceiling for a renewed session grant (design §4「有效期」: 会话
// 授权最长 24 小时). The CLI refuses a longer request locally, so a typo like --for 25h is answered
// here with the number in the message instead of as a server 400. The two must agree
// (internal/httpapi's authSessionGrantMaxSec).
const authGrantRenewMaxSec = 24 * 60 * 60

// NewAuthCmd builds the `auth` command group.
func NewAuthCmd() *gcli.Command {
	whoami := &gcli.Command{
		Name: "whoami",
		Desc: "Show which caller this credential is and the permissions it holds",
		Help: `Prints the caller id, whether it is a root administrator, every permission it holds with
its source (root / config / db) and the grant a database permission came from.

The request carries the terminal session this process belongs to, when the environment names
exactly one of them ($GOFER_SESSION_ID explicitly, or a single agent session variable); the
session grants that apply to it are part of the permissions printed, and the session and its
source are named in the output. With no session, or with several candidates that disagree,
nothing is attached — a request never guesses a session.

  gofer auth whoami
  gofer auth whoami --json`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&authOpts.asJSON, "json", "", false, "print the server's answer as JSON")
		},
		Func: runAuthWhoami,
	}
	routes := &gcli.Command{
		Name: "routes",
		Desc: "List the server's routes and the permissions each one needs",
		Help: `Prints every route of the running server with its declaration: who may call it
(require / in_handler / public / no_user_surface), the permissions it asks for, and which other
credential reaches it (a job credential, a worker token, or the route's own ticket / token).

  gofer auth routes`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&authOpts.asJSON, "json", "", false, "print the server's answer as JSON")
		},
		Func: runAuthRoutes,
	}
	// renew is the one WRITE the CLI makes in the permission model (design T3): a session grant whose
	// deadline is near can be pushed back without a browser. Scope, snapshot, grantor and owner
	// cannot be changed by it — renewing only moves expires_at.
	renew := &gcli.Command{
		Name: "renew",
		Desc: "Give a session grant more time: renew <grant-id> --for <dur> (at most 24h)",
		Help: `Pushes back the deadline of a session grant that is still in force: the new deadline is now
plus --for, at most 24 hours. Only the deadline moves — the granted pattern, what it expands to,
the grantor and the session it is bound to all stay as they are, so a renewal cannot widen a grant.

A grant that already expired, was revoked, no longer matches any permission, or whose session
ended (or changed owner / incarnation) cannot be renewed: the server refuses it and this command
prints its reason — grant it again from the web console instead.

  gofer auth grant renew g-1a2b3c --for 4h
  gofer auth grant renew g-1a2b3c --for 90m`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("id", "grant id (from the web console or GET /v1/auth/grants)", true)
			c.DurationOpt(&authGrantOpts.renewFor, "for", "", 0, "how much longer the grant should last, from now (e.g. 4h, 90m; at most 24h)")
		},
		Func: runAuthGrantRenew,
	}
	tree := &gcli.Command{
		Name: "grant",
		Desc: "Renew a grant that is still in force (creation and revoking happen on the web console)",
		Subs: []*gcli.Command{renew},
	}
	return &gcli.Command{
		Name: "auth",
		Desc: "Inspect permissions: who this credential is, what it holds, what routes need",
		Subs: []*gcli.Command{whoami, routes, tree},
	}
}

// runAuthWhoami runs `gofer auth whoami`.
func runAuthWhoami(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runWhoami(c, cli)
}

// runWhoami is the test seam (a stub server drives it). The session the request carries is the
// client's own business (internal/client's single injection point), so the command prints what the
// client attached and where it came from.
func runWhoami(c *gcli.Command, cli *client.Client) error {
	who, err := cli.Whoami()
	if err != nil {
		return authFail("%v", err)
	}
	if authOpts.asJSON {
		return printTrackerJSON(c, who)
	}
	c.Printf("caller:      %s\n", orNone(who.CallerID))
	if who.CallerName != "" {
		c.Printf("name:        %s\n", who.CallerName)
	}
	c.Printf("kind:        %s\n", who.Kind)
	c.Printf("root:        %s\n", yesNoLabel(who.Root))
	if sid, source := cli.SessionID(); who.SessionID != "" || sid != "" {
		shown := who.SessionID
		if shown == "" {
			shown = sid
		}
		if source == "" {
			// The server reports a session id this command did not attach (an explicit
			// X-Gofer-Session-Id header, e.g. from another client).
			source = "the request"
		}
		c.Printf("session:     %s (from %s)\n", shown, source)
	}
	c.Printf("grants:      version %d\n", who.GrantsVersion)
	if len(who.Roots) > 0 {
		c.Printf("root admins: %s\n", strings.Join(who.Roots, ", "))
	}
	if len(who.Permissions) == 0 {
		c.Print("\nno permissions\n")
	} else {
		c.Printf("\npermissions (%d):\n", len(who.Permissions))
		width := 0
		for _, p := range who.Permissions {
			if len(p.Name) > width {
				width = len(p.Name)
			}
		}
		for _, p := range who.Permissions {
			line := fmt.Sprintf("  %-*s  %-7s", width, p.Name, p.Source)
			if p.GrantID != "" {
				line += " " + p.GrantID
			}
			c.Println(line)
		}
	}
	if who.Hint != "" {
		c.Printf("\nnote: %s\n", who.Hint)
	}
	return nil
}

// runAuthRoutes runs `gofer auth routes`.
func runAuthRoutes(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runRoutes(c, cli)
}

// runRoutes is the test seam.
func runRoutes(c *gcli.Command, cli *client.Client) error {
	routes, err := cli.AuthRoutes()
	if err != nil {
		return authFail("%v", err)
	}
	if authOpts.asJSON {
		return printTrackerJSON(c, map[string]any{"routes": routes})
	}
	if len(routes) == 0 {
		c.Println("the server reported no route")
		return nil
	}
	width := 0
	for _, r := range routes {
		if len(r.Path) > width {
			width = len(r.Path)
		}
	}
	c.Printf("%-7s %-*s %-18s %-32s %s\n", "METHOD", width, "PATH", "DECLARATION", "PERMISSIONS", "OTHER CREDENTIALS")
	for _, r := range routes {
		c.Printf("%-7s %-*s %-18s %-32s %s\n",
			r.Method, width, r.Path, authRouteDeclaration(r),
			strings.Join(r.Permissions, ","), authRouteCredentials(r))
	}
	return nil
}

// authRouteDeclaration renders a route's user-class category the way the permission reference
// does (an InHandler route carries the mode its handler applies).
func authRouteDeclaration(r client.AuthRoute) string {
	switch r.User {
	case "in_handler":
		return "in_handler(" + r.Mode + ")"
	case "require", "public", "no_user_surface", "pending":
		return r.User
	default:
		return orNone(r.User)
	}
}

// authRouteCredentials names the non-user credentials a route accepts, or "-" when it is a
// user-only route.
func authRouteCredentials(r client.AuthRoute) string {
	var parts []string
	if r.JobCredential != "" {
		parts = append(parts, "job")
	}
	if r.WorkerSelf {
		parts = append(parts, "worker")
	}
	if r.External != "" {
		parts = append(parts, "external")
	}
	if r.Static {
		parts = append(parts, "static")
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// runAuthGrantRenew runs `gofer auth grant renew`.
func runAuthGrantRenew(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runGrantRenew(c, cli)
}

// runGrantRenew is the test seam (a stub server drives it): check --for locally, renew, print the
// deadline the server reported. A server refusal (the grant went out of force in the meantime) is
// shown with the server's own reason — it names the condition and says「重新授权」, which is the
// only useful next step.
func runGrantRenew(c *gcli.Command, cli *client.Client) error {
	id := strings.TrimSpace(c.Arg("id").String())
	if id == "" {
		return authFail("give the grant id to renew: gofer auth grant renew <grant-id> --for 4h")
	}
	sec, err := grantRenewSeconds(authGrantOpts.renewFor)
	if err != nil {
		return authFail("%v", err)
	}
	g, err := cli.RenewAuthGrant(id, sec)
	if err != nil {
		return authFail("%v", err)
	}
	c.Printf("grant:       %s\n", orNone(g.ID))
	c.Printf("subject:     %s\n", orNone(g.Subject))
	c.Printf("state:       %s\n", authGrantStateLabel(g))
	// The one field a renewal really changes, in the server's clock (the same formatter the other
	// time columns use) plus the raw stamp, so a reader can check it against a log.
	c.Printf("expires at:  %s (unix %d)\n", fmtServerTime(g.ExpiresAt), g.ExpiresAt)
	return nil
}

// grantRenewSeconds turns --for into the server's expires_sec, refusing what the server would refuse
// anyway (a missing / non-positive duration and anything over the 24-hour ceiling) so the operator
// gets the local answer with the number in it.
func grantRenewSeconds(d time.Duration) (int64, error) {
	if d <= 0 {
		return 0, fmt.Errorf("--for is required: say how much longer the grant should last (e.g. --for 4h)")
	}
	sec := int64(d / time.Second)
	if sec < 1 {
		return 0, fmt.Errorf("--for %s is too short: give at least 1s", d)
	}
	if sec > authGrantRenewMaxSec {
		return 0, fmt.Errorf("--for %s is too long: a session grant lasts at most 24h", d)
	}
	return sec, nil
}

// authGrantStateLabel renders the renewed grant's state, marking the one thing a renewal is for:
// that the grant is in force right after it (the server only renews such a grant, so a「not in
// force」 here would mean the server changed its mind between the two requests).
func authGrantStateLabel(g client.AuthGrant) string {
	state := orNone(g.State)
	if g.Effective {
		return state + " (in force)"
	}
	return state
}

// yesNoLabel renders a boolean the way the other CLI views do.
func yesNoLabel(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

// orNone renders an empty value as "-" so a column never looks broken.
func orNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "-"
	}
	return v
}
