package streaming

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/wsproto"
)

// TestACPMirrorNotice (gofer-e2x7): a live remote job whose structured record cannot
// reach this host is explained up front instead of leaving the reader waiting; a job
// whose record does arrive (local, a v22+ worker) or might still (an offline worker)
// gets no notice.
func TestACPMirrorNotice(t *testing.T) {
	cfg := &config.Config{Runners: map[string]config.RunnerConfig{
		"w":    {Type: "worker", WorkerID: "w1"},
		"peer": {Type: "peer-http"},
	}}
	protos := map[string]int{"w1": wsproto.ACPMirrorMinProtocolVersion - 1, "w2": wsproto.ACPMirrorMinProtocolVersion}
	proto := func(id string) (int, bool) {
		p, ok := protos[id]
		return p, ok
	}
	cases := []struct {
		name string
		res  job.JobResult
		code string
	}{
		{"local job", job.JobResult{Runner: "local"}, ""},
		{"peer job", job.JobResult{Runner: "peer"}, "acp_mirror_peer"},
		{"old worker via runner default", job.JobResult{Runner: "w"}, "acp_mirror_unsupported"},
		{"new worker", job.JobResult{Runner: "w", WorkerID: "w2"}, ""},
		{"offline worker", job.JobResult{Runner: "w", WorkerID: "w3"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := acpMirrorNotice(cfg, tc.res, proto)
			code, _ := got["code"].(string)
			if code != tc.code {
				t.Fatalf("notice = %v, want code %q", got, tc.code)
			}
			if got != nil && (got["kind"] != "notice" || got["text"] == "") {
				t.Fatalf("notice = %v, want kind=notice with a text", got)
			}
		})
	}
	if got := acpMirrorNotice(cfg, job.JobResult{Runner: "w"}, nil); got != nil {
		t.Fatalf("no hub must mean no worker notice, got %v", got)
	}
	if got := acpMirrorNotice(nil, job.JobResult{Runner: "peer"}, proto); got != nil {
		t.Fatalf("nil config must mean no notice, got %v", got)
	}
}
