package job

import (
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func newTimeoutLimitService(t *testing.T, projectMax, serverMax int) *Service {
	t.Helper()
	root := t.TempDir()
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Server:  config.ServerConfig{MaxJobTimeoutSec: serverMax},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"exec"}, AllowedRunners: []string{"local"}, AllowExec: true, MaxTimeoutSec: projectMax},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

func TestTimeoutLimitSourceReported(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		projectMax, serverMax int
		timeout               int
		wantSec               int
		wantClamped           bool
		wantSource, wantKey   string
	}{
		{"server limit clamps", 0, 100, 500, 100, true, config.TimeoutLimitServer, "server.max_job_timeout_sec"},
		{"project limit clamps", 80, 100, 500, 80, true, config.TimeoutLimitProject, "projects.self.max_timeout_sec"},
		{"default server limit", 0, 0, 100000, config.DefaultMaxJobTimeoutSec, true, config.TimeoutLimitServer, "server.max_job_timeout_sec"},
		{"not clamped still names the limit", 0, 100, 50, 50, false, config.TimeoutLimitServer, "server.max_job_timeout_sec"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTimeoutLimitService(t, tc.projectMax, tc.serverMax)
			final := submitAndWait(t, s, JobRequest{
				ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "env-print"), Cwd: ".", TimeoutSec: tc.timeout,
			})
			if final.TimeoutSec != tc.wantSec || final.TimeoutClamped != tc.wantClamped ||
				final.TimeoutLimitSource != tc.wantSource || final.TimeoutLimitKey != tc.wantKey {
				t.Fatalf("result = (sec %d clamped %v source %q key %q), want (%d %v %q %q)",
					final.TimeoutSec, final.TimeoutClamped, final.TimeoutLimitSource, final.TimeoutLimitKey,
					tc.wantSec, tc.wantClamped, tc.wantSource, tc.wantKey)
			}
			// The source survives a round trip through the store (the key is derived from it).
			rec, ok, err := s.meta.GetJob(final.ID)
			if err != nil || !ok || rec.TimeoutLimitSource != tc.wantSource {
				t.Fatalf("persisted source = %q ok=%v err=%v, want %q", rec.TimeoutLimitSource, ok, err, tc.wantSource)
			}
			back := fromRecord(rec)
			if back.TimeoutLimitSource != tc.wantSource || back.TimeoutLimitKey != tc.wantKey {
				t.Fatalf("read back = %q / %q", back.TimeoutLimitSource, back.TimeoutLimitKey)
			}
		})
	}
}

func TestStrictTimeoutRefusesToClamp(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                  string
		projectMax, serverMax int
		wantInErr             []string
	}{
		{"server", 0, 100, []string{"timeout 500s", "limit 100s", "server.max_job_timeout_sec"}},
		{"project", 80, 100, []string{"timeout 500s", "limit 80s", "projects.self.max_timeout_sec"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTimeoutLimitService(t, tc.projectMax, tc.serverMax)
			req := JobRequest{ProjectKey: "self", Agent: "exec", Runner: "local", Cmd: testcmd.Cmd(t, "env-print"), Cwd: ".", TimeoutSec: 500, StrictTimeout: true}
			_, err := s.Submit(req)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("strict over-limit Submit err = %v, want ErrInvalidRequest", err)
			}
			for _, w := range tc.wantInErr {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
			// Within the limit strict mode changes nothing.
			req.TimeoutSec = 50
			if final := submitAndWait(t, s, req); final.TimeoutClamped || final.TimeoutSec != 50 {
				t.Fatalf("strict within limit: %+v", final)
			}
		})
	}
}
