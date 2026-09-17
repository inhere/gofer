package acptest

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
