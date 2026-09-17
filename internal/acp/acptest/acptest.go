package acptest

import (
	"strconv"
	"strings"
)

// CmdArgs returns the testcmd argv (after the binary path) that launches the fake
// ACP agent: `gofer-testcmd acp-fake [flags]`. Pair it with testcmd.Path(t) as the
// agent's Command, or use them as argv[1:] for a direct acp.Start.
func CmdArgs(o Options) []string {
	args := []string{"acp-fake"}
	if o.StopReason != "" {
		args = append(args, "--stop-reason", o.StopReason)
	}
	if o.Slow {
		args = append(args, "--slow")
	}
	if o.RefuseLoad {
		args = append(args, "--refuse-load")
	}
	if o.Delay > 0 {
		args = append(args, "--delay", o.Delay.String())
	}
	if o.PermissionKind != "" {
		args = append(args, "--perm-kind", o.PermissionKind)
	}
	if len(o.PermissionOptions) > 0 {
		args = append(args, "--perm-options", strings.Join(o.PermissionOptions, ","))
	}
	if o.PermissionRepeats > 0 {
		args = append(args, "--perm-repeats", strconv.Itoa(o.PermissionRepeats))
	}
	return args
}

// Option ids the fake agent offers in session/request_permission, with the ACP
// option kind each one carries.
const (
	AllowOnceOptionID    = "allow-once-id"
	AllowAlwaysOptionID  = "allow-always-id"
	RejectOnceOptionID   = "reject-once-id"
	RejectAlwaysOptionID = "reject-always-id"
)

// PermissionTitle is the title of the scripted permission request's tool call, and
// PermissionKindDefault its kind when Options.PermissionKind is unset (a workspace
// mutation — the kind the approval gate asks a human about).
const (
	PermissionTitle       = "Write main.go"
	PermissionKindDefault = "edit"
)
