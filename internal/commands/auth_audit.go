package commands

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// authAuditOpts holds the flags of `gofer auth audit` (registered in flagGlobals, G053).
var authAuditOpts struct {
	caller string
	route  string
	status string
	since  string
	until  string
	cursor int64
	limit  int
	asJSON bool
}

// newAuthAuditCmd builds `gofer auth audit`: the admin audit trail of GET /v1/audit.
func newAuthAuditCmd() *gcli.Command {
	return &gcli.Command{
		Name: "audit",
		Desc: "Show the admin audit trail: who called which write route, when, and how it ended",
		Help: `Prints the admin audit, newest first: every write request (refusals included), with the
caller, the route, the target resource, the status and the client IP. Request bodies and query
values are never recorded. Needs the audit.read permission.

--since / --until take a duration back from now (24h), a date (2026-12-31), an RFC 3339 time or
unix seconds. When more lines exist, the output ends with the --cursor value of the next page.

  gofer auth audit
  gofer auth audit --status fail --since 24h
  gofer auth audit --caller ops --route /v1/schedules --limit 50`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.StrOpt(&authAuditOpts.caller, "caller", "", "", "only this caller id")
			c.StrOpt(&authAuditOpts.route, "route", "", "", "only routes containing this text (e.g. /v1/schedules)")
			c.StrOpt(&authAuditOpts.status, "status", "", "", "ok (status < 400) or fail (status >= 400)")
			c.StrOpt(&authAuditOpts.since, "since", "", "", "not before: a duration back from now (24h), a date, an RFC 3339 time or unix seconds")
			c.StrOpt(&authAuditOpts.until, "until", "", "", "not after (same forms as --since)")
			c.Int64Opt(&authAuditOpts.cursor, "cursor", "", 0, "page cursor printed by the previous page")
			c.IntOpt(&authAuditOpts.limit, "limit", "", 0, "how many lines to show (default 100, at most 500)")
			c.BoolOpt(&authAuditOpts.asJSON, "json", "", false, "print the server's answer as JSON")
		},
		Func: runAuthAudit,
	}
}

func runAuthAudit(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return authFail("%v", err)
	}
	return runAdminAudit(c, cli, time.Now())
}

// auditTimeArg turns a `1h` style duration into unix seconds back from now; any other form
// is left for the server to parse.
func auditTimeArg(raw string, now time.Time) string {
	raw = strings.TrimSpace(raw)
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return strconv.FormatInt(now.Add(-d).Unix(), 10)
	}
	return raw
}

// runAdminAudit is the test seam.
func runAdminAudit(c *gcli.Command, cli *client.Client, now time.Time) error {
	page, err := cli.ListAdminAudit(client.AdminAuditQuery{
		Caller: authAuditOpts.caller, Route: authAuditOpts.route, Status: authAuditOpts.status,
		Since: auditTimeArg(authAuditOpts.since, now), Until: auditTimeArg(authAuditOpts.until, now),
		Cursor: authAuditOpts.cursor, Limit: authAuditOpts.limit,
	})
	if err != nil {
		return authFail("%v", err)
	}
	if authAuditOpts.asJSON {
		raw, err := json.MarshalIndent(page, "", "  ")
		if err != nil {
			return authFail("%v", err)
		}
		c.Println(string(raw))
		return nil
	}
	if len(page.Entries) == 0 {
		c.Println("no matching audit line")
		return nil
	}
	for _, e := range page.Entries {
		who := e.CallerID
		if who == "" {
			who = "-"
		}
		line := callerAuditTime(e.At) + "  " + padRight(who, 18) + " " + strconv.Itoa(e.Status) + "  " + e.Route
		if e.Target != "" {
			line += "  " + e.Target
		}
		if e.Error != "" {
			line += "  (" + e.Error + ")"
		}
		c.Println(line)
	}
	if page.NextCursor > 0 {
		c.Printf("more: --cursor %d\n", page.NextCursor)
	}
	return nil
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(s))
}
