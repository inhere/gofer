package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// The context-window level a structured stream reports (suga's turn_end.context) reaches
// the job's usage, persisted with it: the background-agent rotation reads it from there.
func TestJobUsageCarriesTheContextLevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	lines := []string{
		`{"type":"session","session_id":"sess-ctx-1"}`,
		`{"type":"turn_end","turn":1,"context":{"est_tokens":12000,"window":200000,"ratio":0.06}}`,
		`{"type":"turn_end","turn":2,"context":{"est_tokens":150000,"window":200000,"ratio":0.75}}`,
		`{"type":"result","result":"answer","usage":{"input_tokens":1200,"output_tokens":400,"cost_usd":0.01}}`,
	}
	samplePath := filepath.Join(root, "suga-sample.ndjson")
	if err := os.WriteFile(samplePath, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"suga"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			// Keyed "suga" so the built-in suga stream rules apply; the command replays the sample.
			"suga": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"cat-file", samplePath},
				OutputFormat: config.OutputFormatNDJSON},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "p", Cwd: ".", TimeoutSec: 30})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s)", final.Status, final.Error)
	}
	check := func(label string, u *runner.Usage) {
		t.Helper()
		if u == nil || u.InputTokens != 1200 || u.CostUSD != 0.01 {
			t.Fatalf("%s usage = %+v", label, u)
		}
		c := u.Context
		if c == nil || c.Ratio != 0.75 || c.UsedTokens != 150000 || c.LimitTokens != 200000 || c.Source != runner.ContextSourceTurnEnd {
			t.Fatalf("%s context = %+v, want the LAST turn_end level", label, c)
		}
	}
	check("final", final.Usage)
	persisted, ok := s.Get(final.ID)
	if !ok {
		t.Fatal("job missing")
	}
	check("persisted", persisted.Usage)
}
