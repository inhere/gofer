package commands

import (
	"fmt"
	"os"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// auth.go implements `gofer auth`: what THIS credential holds (`whoami`) and what the running
// server asks of every route (`routes`). It is a read-only window into the permission model
// (design「接口」一节, plan T2.1) — grants are managed on the web console, and the CLI's job is to
// answer "why was I refused?" without a browser:
//
//	gofer auth whoami   — the caller, whether it is a root administrator, every permission it
//	                      holds and where each one came from
//	gofer auth routes   — the route table of the running binary, i.e. what each route needs
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

// NewAuthCmd builds the `auth` command group.
func NewAuthCmd() *gcli.Command {
	whoami := &gcli.Command{
		Name: "whoami",
		Desc: "Show which caller this credential is and the permissions it holds",
		Help: `Prints the caller id, whether it is a root administrator, every permission it holds with
its source (root / config / db) and the grant a database permission came from.

A user-class caller's sessions are attached with $GOFER_SESSION_ID when that variable is set;
the session grants that apply to it are part of the next batch of the permission model.

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
	return &gcli.Command{
		Name: "auth",
		Desc: "Inspect permissions: who this credential is, what it holds, what routes need",
		Subs: []*gcli.Command{whoami, routes},
	}
}

// runAuthWhoami runs `gofer auth whoami`.
func runAuthWhoami(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runWhoami(c, cli, serverSessionID())
}

// serverSessionID is the terminal session a CLI request attaches: an explicitly set
// `GOFER_SESSION_ID`. The general rule (and the other session variables) belongs to the
// session-grant batch; an explicitly set variable is already unambiguous, so whoami reports the
// session a request really carried.
func serverSessionID() string {
	return strings.TrimSpace(os.Getenv("GOFER_SESSION_ID"))
}

// runWhoami is the test seam (a stub server drives it).
func runWhoami(c *gcli.Command, cli *client.Client, sessionID string) error {
	who, err := cli.Whoami(sessionID)
	if err != nil {
		return authFail("%v", err)
	}
	if authOpts.asJSON {
		return printTrackerJSON(c, who)
	}
	c.Printf("caller:      %s\n", orNone(who.CallerID))
	c.Printf("kind:        %s\n", who.Kind)
	c.Printf("root:        %s\n", yesNoLabel(who.Root))
	if who.SessionID != "" {
		source := "GOFER_SESSION_ID"
		if sessionID == "" {
			// The server reports a session id this command did not attach (an explicit
			// X-Gofer-Session-Id header, e.g. from another client).
			source = "the request"
		}
		c.Printf("session:     %s (from %s)\n", who.SessionID, source)
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
