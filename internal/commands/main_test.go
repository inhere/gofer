package commands

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestMain isolates the package from process-level state a test could otherwise
// inherit from the developer's shell or from the order tests run in:
//   - the server time zone is never fetched over the network (without this a test
//     that formats a timestamp before any other test cached it hits GET /v1/stats);
//   - GOFER_*_TOKEN variables of the host are dropped;
//   - the pristine flag option structs are snapshotted for resetFlagGlobals.
func TestMain(m *testing.M) {
	serverTZFetch = func() (int, bool) { return 0, false }
	// The developer's own user-level relay hooks must not change prime output.
	primeUserHome = func() (string, error) { return "", errors.New("no user home in tests") }
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GOFER_") && strings.HasSuffix(name, "_TOKEN") {
			_ = os.Unsetenv(name)
		}
	}
	snapshotFlagGlobals()
	os.Exit(m.Run())
}
