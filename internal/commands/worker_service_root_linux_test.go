//go:build linux

package commands

import (
	"strings"
	"testing"
)

// A worker service is a user-level registration: registering it as root is refused,
// and any other uid passes the check.
func TestWorkerServiceRefusesRoot(t *testing.T) {
	prev := workerServiceEUID
	t.Cleanup(func() { workerServiceEUID = prev })

	workerServiceEUID = func() int { return 0 }
	err := workerServiceRefuseRoot()
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("as root: err = %v, want a refusal naming root", err)
	}
	workerServiceEUID = func() int { return 1000 }
	if err := workerServiceRefuseRoot(); err != nil {
		t.Fatalf("as uid 1000: err = %v, want nil", err)
	}
}
