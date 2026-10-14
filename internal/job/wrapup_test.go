package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// newSugaWrapUpService builds a service whose "suga" agent replays a suga
// `--output-format stream-json` fixture (gofer-gk74).
func newSugaWrapUpService(t *testing.T, lines ...string) *Service {
	t.Helper()
	root := t.TempDir()
	sample := filepath.Join(root, "suga-sample.ndjson")
	if err := os.WriteFile(sample, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"suga"}, AllowedRunners: []string{"local"}, AllowExec: true},
		},
		Agents: map[string]config.AgentConfig{
			"suga": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"cat-file", sample}, OutputFormat: config.OutputFormatNDJSON},
		},
	}
	return newServiceFromCfg(t, root, cfg)
}

// TestSugaWrapUpRecordedOnJob (gofer-gk74): a run that wrapped up on the agent's own step
// ceiling still ends `done` with a report — the job must say why the agent stopped, so a
// reader does not take the report for a finished task.
func TestSugaWrapUpRecordedOnJob(t *testing.T) {
	t.Parallel()
	s := newSugaWrapUpService(t,
		`{"type":"session","id":"s-1"}`,
		`{"type":"turn_start"}`,
		`{"type":"result","result":"汇报正文","wrap_up":"max_steps"}`,
	)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%q), want done", final.Status, final.Error)
	}
	if final.WrapUp != ndjsonfilter.WrapUpMaxSteps {
		t.Fatalf("wrap_up = %q, want %q", final.WrapUp, ndjsonfilter.WrapUpMaxSteps)
	}
}

// TestSugaWrapUpFromErrorRow: a run killed by the ceiling before suga reported the field
// leaves only the error row — gofer reads it as the same fact (suga-p5mt).
func TestSugaWrapUpFromErrorRow(t *testing.T) {
	t.Parallel()
	s := newSugaWrapUpService(t,
		`{"type":"turn_start"}`,
		`{"type":"error","error":"max steps exceeded: 100"}`,
	)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
	})
	if final.WrapUp != ndjsonfilter.WrapUpMaxSteps {
		t.Fatalf("wrap_up = %q, want %q (from the error row)", final.WrapUp, ndjsonfilter.WrapUpMaxSteps)
	}
}

// TestSugaNormalAnswerHasNoWrapUp: an ordinary answer carries no reason — a job must not
// look wrapped up just because it ran on a dialect that CAN report one.
func TestSugaNormalAnswerHasNoWrapUp(t *testing.T) {
	t.Parallel()
	s := newSugaWrapUpService(t,
		`{"type":"session","id":"s-1"}`,
		`{"type":"result","result":"汇报正文"}`,
	)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 60,
	})
	if final.WrapUp != "" {
		t.Fatalf("wrap_up = %q, want empty for a normal answer", final.WrapUp)
	}
}

// TestWrapUpText: the human line names the ceiling and says the task may be unfinished;
// an unknown reason is still shown.
func TestWrapUpText(t *testing.T) {
	if got := WrapUpText(ndjsonfilter.WrapUpMaxSteps); !strings.Contains(got, "步数上限") {
		t.Fatalf("max_steps text = %q", got)
	}
	if got := WrapUpText(ndjsonfilter.WrapUpTimeout); !strings.Contains(got, "时限") {
		t.Fatalf("timeout text = %q", got)
	}
	if got := WrapUpText("wall_clock"); !strings.Contains(got, "wall_clock") {
		t.Fatalf("unknown text = %q", got)
	}
	if got := WrapUpText(""); got != "" {
		t.Fatalf("empty text = %q", got)
	}
}
