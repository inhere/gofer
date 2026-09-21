package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/xfer"
)

// TestSubmitJobUploadsRequireStagedID: the job's uploads name transfers that must
// ALREADY be staged on this server — the HTTP body carries xfer ids, never local
// paths (a path would have the server read the submitter's filesystem, which is
// exactly what XFER-01's staging area exists to avoid). An unknown id or a
// cross-project one is a 400 before anything is admitted.
func TestSubmitJobUploadsRequireStagedID(t *testing.T) {
	s, mgr, _ := newXferServer(t, config.ServerConfig{Token: "tok"}, xfer.Limits{})
	rec, err := mgr.StagePut("tester", "local", "demo", "tmp/in/a.bin", 4, "", false)
	if err != nil {
		t.Fatalf("StagePut: %v", err)
	}
	other, err := mgr.StagePut("tester", "local", "elsewhere", "tmp/in/b.bin", 4, "", false)
	if err != nil {
		t.Fatalf("StagePut: %v", err)
	}

	cases := []struct {
		name   string
		upload job.UploadSpec
		want   int
	}{
		{"a local path is not an id", job.UploadSpec{XferID: "./a.bin:tmp/in/a.bin", Dest: "tmp/in/a.bin"}, http.StatusBadRequest},
		{"an unknown id", job.UploadSpec{XferID: "no-such-xfer", Dest: "tmp/in/a.bin"}, http.StatusBadRequest},
		{"another project's transfer", job.UploadSpec{XferID: other.ID, Dest: "tmp/in/b.bin"}, http.StatusBadRequest},
		{"a staged transfer of this project", job.UploadSpec{XferID: rec.ID, Dest: "tmp/in/a.bin"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, http.MethodPost, "/v1/jobs", "tok", job.JobRequest{
				ProjectKey: "demo", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
				Cmd:     []string{"go", "version"},
				Uploads: []job.UploadSpec{tc.upload},
			})
			defer resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}
