package mcpserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/job"
)

// A submitted job's MCP view reports the clamp and which limit applied.
func TestJobViewCarriesTimeoutLimit(t *testing.T) {
	raw, err := json.Marshal(toJobView(job.JobResult{
		ID: "j", TimeoutSec: 3600, RequestedTimeoutSec: 10800, TimeoutClamped: true,
		TimeoutLimitSource: "server", TimeoutLimitKey: "server.max_job_timeout_sec",
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"timeout_sec":3600`, `"requested_timeout_sec":10800`, `"timeout_clamped":true`,
		`"timeout_limit_source":"server"`, `"timeout_limit_key":"server.max_job_timeout_sec"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("view %s lacks %s", raw, want)
		}
	}
}
