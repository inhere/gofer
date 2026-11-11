package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

func newTurnsService(t *testing.T, agentKey string, sample []string) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	samplePath := filepath.Join(root, "sample.ndjson")
	if err := os.WriteFile(samplePath, []byte(strings.Join(sample, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{agentKey}, AllowedRunners: []string{"local"}, Secrets: []string{"tok"}},
		},
		Agents: map[string]config.AgentConfig{
			agentKey: {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"cat-file", samplePath}, OutputFormat: config.OutputFormatNDJSON},
		},
		Secrets: map[string]config.SecretConfig{"tok": {Env: "TURNS_TEST_SECRET"}},
	}
	return newServiceFromCfg(t, root, cfg), root
}

func readTurnsFile(t *testing.T, resultDir string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(resultDir, store.TurnsFile))
	if err != nil {
		return nil
	}
	return b
}

// A suga job writes turns.jsonl while it runs: two turns, deltas accumulated, and a
// granted secret split across two deltas appears only masked.
func TestJobSugaWritesMaskedTurnsFile(t *testing.T) {
	const secret = "ghp_turns_secret_0123456789"
	t.Setenv("TURNS_TEST_SECRET", secret)
	s, _ := newTurnsService(t, "suga", []string{
		`{"type":"session","id":"s1","session_id":"s1"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_delta","text":"the token ghp_turns_sec"}`,
		`{"type":"message_delta","text":"ret_0123456789 is set"}`,
		`{"type":"tool_call","name":"bash","args":"ls"}`,
		`{"type":"turn_end","usage":{"total_tokens":9}}`,
		`{"type":"turn_start"}`,
		`{"type":"message_delta","text":"second turn"}`,
		`{"type":"turn_end"}`,
		`{"type":"result","result":"second turn"}`,
	})
	final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "suga", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 30, Secrets: []string{"tok"}})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (%s)", final.Status, final.Error)
	}
	b := readTurnsFile(t, final.ResultDir)
	if b == nil {
		t.Fatal("turns.jsonl was not written")
	}
	if strings.Contains(string(b), secret) || strings.Contains(string(b), "ret_0123456789") {
		t.Fatalf("turns.jsonl leaked the secret: %s", b)
	}
	lines := splitLogLines(string(b))
	if len(lines) != 2 || !strings.Contains(lines[0], `"text":"the token *** is set"`) || !strings.Contains(lines[0], `"bash":1`) || !strings.Contains(lines[1], `"text":"second turn"`) {
		t.Fatalf("turns.jsonl = %s", b)
	}
}

// An agent without a registered turn dialect (omp) writes no turns.jsonl.
func TestJobOMPWritesNoTurnsFile(t *testing.T) {
	s, _ := newTurnsService(t, "omp", ndjsonSample())
	final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "omp", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 30})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (%s)", final.Status, final.Error)
	}
	if b := readTurnsFile(t, final.ResultDir); b != nil {
		t.Fatalf("turns.jsonl written for an agent without a turn dialect: %s", b)
	}
}

// An ACP job that is granted a secret never writes it to acp.jsonl (the prompt echoes it).
func TestJobACPEventStreamIsMaskedWithGrantedSecret(t *testing.T) {
	const secret = "ghp_acp_secret_0123456789"
	t.Setenv("TURNS_TEST_SECRET", secret)
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	cfg := s.cfg.Load()
	cfg.Secrets = map[string]config.SecretConfig{"tok": {Env: "TURNS_TEST_SECRET"}}
	p := cfg.Projects["self"]
	p.Secrets = []string{"tok"}
	cfg.Projects["self"] = p
	final := submitAndWait(t, s, JobRequest{ProjectKey: "self", Agent: "acpbot", Runner: "local", Prompt: "use " + secret, Cwd: ".", TimeoutSec: 30, Secrets: []string{"tok"}})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (%s)", final.Status, final.Error)
	}
	b, err := os.ReadFile(filepath.Join(final.ResultDir, "artifacts", "acp.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) || !strings.Contains(string(b), "use ***") {
		t.Fatalf("acp.jsonl = %s", b)
	}
}

// A recovered ACP session whose granted secret can no longer be resolved is not
// resumed (it would write the value unmasked); the caller's recovery-failure handling
// applies.
func TestResumeLocalSessionRefusesWhenSecretCannotBeResolved(t *testing.T) {
	t.Setenv("TURNS_TEST_SECRET", "")
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{})
	cfg := s.cfg.Load()
	cfg.Secrets = map[string]config.SecretConfig{"tok": {Env: "TURNS_TEST_SECRET"}}
	p := cfg.Projects["self"]
	p.Secrets = []string{"tok"}
	cfg.Projects["self"] = p
	rec := jobstore.JobRecord{
		ID: "job-recover-secret", ProjectKey: "self", Agent: "acpbot", Cwd: root,
		ResultDir: filepath.Join(root, "self", "job-recover-secret"), SessionID: "sess-1",
		RequestJSON: `{"project_key":"self","agent":"acpbot","runner":"local","secrets":["tok"]}`,
	}
	err := s.resumeLocalSession(rec)
	if err == nil || !strings.Contains(err.Error(), "reload secrets") {
		t.Fatalf("resumeLocalSession err = %v, want a secrets error", err)
	}
	s.mu.Lock()
	_, active := s.jobs[rec.ID]
	s.mu.Unlock()
	if active {
		t.Fatal("the session must not be registered when its secrets cannot be resolved")
	}
}
