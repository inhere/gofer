package httpapi

import (
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// TestSessionInjectorMessengerJobStdoutExcludesStderr pins the T4 seam: the relay reads
// the messenger receipt from Stdout ALONE, so a warning on stderr (which `jobOutput`
// merges into Output, and which is what a failure reason is reported from) must never
// reach it — a receipt mentioned by a warning used to turn a lost message into a
// delivered one.
func TestSessionInjectorMessengerJobStdoutExcludesStderr(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, testToken, false)
	x := sessionInjector{jobs: s.jobs}

	stdoutJob := runExecJob(t, s, []string{testcmd.Path(t), "printf", "sent"})
	done, status, code, output, stdout, err := x.MessengerJob(stdoutJob.ID)
	if err != nil || !done || status != "done" || code != 0 {
		t.Fatalf("MessengerJob() = %v %q %d, %v", done, status, code, err)
	}
	if stdout != "sent" || !strings.Contains(output, "sent") {
		t.Fatalf("stdout = %q, output = %q; want the receipt in both", stdout, output)
	}

	stderrJob := runExecJob(t, s, []string{testcmd.Path(t), "stderr-exit", "0", "已发送"})
	done, status, code, output, stdout, err = x.MessengerJob(stderrJob.ID)
	if err != nil || !done || status != "done" || code != 0 {
		t.Fatalf("MessengerJob() = %v %q %d, %v", done, status, code, err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("stdout = %q, want no stdout — stderr is what printed", stdout)
	}
	if !strings.Contains(output, "已发送") {
		t.Fatalf("output = %q, want the stderr line merged in (the failure-reason channel)", output)
	}
}
