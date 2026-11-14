package commands

import (
	"net"
	"time"

	"github.com/gookit/goutil/errorx"
)

// Process exit codes of `serve status`. 0 and 3 follow LSB status (running / not
// running); 1 (the check itself failed) is the generic error code; 4 and 5 are
// gofer's own.
const (
	serveStatusExitOK       = 0
	serveStatusExitCheck    = 1
	serveStatusExitDown     = 3
	serveStatusExitDegraded = 4
	serveStatusExitUnconfig = 5

	serveStatusStateOK       = "running"
	serveStatusStateDown     = "stopped"
	serveStatusStateDegraded = "degraded"
	serveStatusStateUnconfig = "unconfigured"
)

// managedPortListening reports whether anything accepts connections on host:port.
// It is a seam so tests do not depend on real listeners.
var managedPortListening = func(host, port string) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// serveStatusJSON is the `serve status --json` document: the inspected fields plus
// the derived verdict.
type serveStatusJSON struct {
	serveStatusOutput
	State    string `json:"state"`
	ExitCode int    `json:"exit_code"`
}

// classifyServeStatus maps the inspected fields onto a state name and the process
// exit code.
func classifyServeStatus(out serveStatusOutput) (state string, code int) {
	if !out.Registered {
		// No spec at all: nothing was ever registered. A spec whose native
		// registration vanished is a registered-but-not-running service.
		if out.ConfigFile == "" {
			return serveStatusStateUnconfig, serveStatusExitUnconfig
		}
		return serveStatusStateDown, serveStatusExitDown
	}
	if !out.Active || out.ServerPID <= 0 {
		return serveStatusStateDown, serveStatusExitDown
	}
	if !out.ServerVerified {
		return serveStatusStateDegraded, serveStatusExitDegraded
	}
	if !out.PortOwned {
		if !out.portListening {
			return serveStatusStateDown, serveStatusExitDown
		}
		return serveStatusStateDegraded, serveStatusExitDegraded
	}
	if out.Health != "healthy" || out.VersionAuthMismatch != nil {
		return serveStatusStateDegraded, serveStatusExitDegraded
	}
	if out.RegisteredVersion != "" && out.RunningVersion != "" && out.RegisteredVersion != out.RunningVersion {
		return serveStatusStateDegraded, serveStatusExitDegraded
	}
	return serveStatusStateOK, serveStatusExitOK
}

// serveStatusExitError carries a non-zero verdict as a coded error: gcli turns an
// errorx.ErrorCoder into the process exit code, so the command never os.Exits.
func serveStatusExitError(state string, code int) error {
	if code == serveStatusExitOK {
		return nil
	}
	return errorx.Failf(code, "serve status: state=%s", state)
}

// serveStatusCheckError gives a failed check (no manager, unreadable spec or native
// registration) the documented exit code 1 instead of gcli's generic error code.
func serveStatusCheckError(err error) error {
	return errorx.Failf(serveStatusExitCheck, "serve status: %v", err)
}
