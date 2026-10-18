package serve

import (
	"os"
	"strings"
	"testing"
)

// The server must send through core's mailbox (one service, one set of sender limits): a source
// guard, because assembling a whole serve process in a unit test is not worth it.
func TestServeInjectsCoreMailbox(t *testing.T) {
	b, err := os.ReadFile("serve.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "srv.SetMail(cr.Mail)") {
		t.Fatal("serve.go no longer injects core's mail service into the HTTP server (srv.SetMail(cr.Mail))")
	}
}
