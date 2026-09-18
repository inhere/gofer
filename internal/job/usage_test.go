package job

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/acp/acptest"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

// ompUsageSample is a minimal `omp --mode json` run whose final assistant message
// carries the run's token/cost tally (SUP-01 E): the capture must read it off the
// projected stream and persist it with the job.
func ompUsageSample() []string {
	return []string{
		`{"type":"session","id":"` + ndjsonSampleSessionID + `"}`,
		`{"type":"message_end","turn":1,"message":{"role":"assistant","content":[{"type":"text","text":"first pass"}],"usage":{"inputTokens":100,"outputTokens":20,"totalTokens":120,"cost":{"total":0.0004}}}}`,
		`{"type":"message_end","turn":2,"message":{"role":"assistant","content":[{"type":"text","text":"the answer"}],"usage":{"inputTokens":12000,"outputTokens":3800,"cacheReadTokens":289000,"cacheWriteTokens":0,"totalTokens":304800,"cost":{"total":0.0032}}}}`,
		`{"type":"turn_end","turn":2,"usage":{"input_tokens":12000,"output_tokens":3800,"total_tokens":304800}}`,
		`{"type":"agent_end","turns":2,"reason":"completed"}`,
	}
}

// TestNDJSONUsageRecordedOnResult: an ndjson agent job's usage lands on its result
// with the source naming where it came from, and SURVIVES the terminal write (the
// entry is evicted, so the read below goes through jobs.usage_json).
func TestNDJSONUsageRecordedOnResult(t *testing.T) {
	root := t.TempDir()
	samplePath := filepath.Join(root, "omp-usage.ndjson")
	if err := os.WriteFile(samplePath, []byte(strings.Join(ompUsageSample(), "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write sample: %v", err)
	}

	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"omp"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			"omp": {
				Type:         agent.TypeCLIAgent,
				Command:      testcmd.Path(t),
				Args:         []string{"cat-file", samplePath},
				OutputFormat: config.OutputFormatNDJSON,
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "omp", Runner: "local",
		Prompt: "list the project root", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Usage == nil {
		t.Fatal("no usage on the job result")
	}
	if final.Usage.Source != runner.UsageSourceNDJSONOMP {
		t.Fatalf("usage.source = %q, want %q", final.Usage.Source, runner.UsageSourceNDJSONOMP)
	}
	if final.Usage.InputTokens != 12000 || final.Usage.OutputTokens != 3800 ||
		final.Usage.CacheReadTokens != 289000 || final.Usage.TotalTokens != 304800 || final.Usage.CostUSD != 0.0032 {
		t.Fatalf("usage = %+v, want the LAST assistant message's tally", *final.Usage)
	}

	persisted, ok := s.Get(final.ID)
	if !ok {
		t.Fatalf("job %s not found in the store", final.ID)
	}
	if persisted.Usage == nil || *persisted.Usage != *final.Usage {
		t.Fatalf("persisted usage = %+v, want %+v", persisted.Usage, final.Usage)
	}
}

// TestCodexStderrTokensParsed: codex `exec` prints its token count to stderr after
// the run (`tokens used\n19,802`), so the terminal capture reads it there. The
// sniff is gated on the agent being codex — the identical text from another agent
// is content, not a token count, and must not fabricate usage.
func TestCodexStderrTokensParsed(t *testing.T) {
	root := t.TempDir()
	// The exact two lines codex writes at the end of a run.
	codexTail := "tokens used\n19,802"
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"codex", "omp"}, AllowedRunners: []string{"local"}},
		},
		Agents: map[string]config.AgentConfig{
			"codex": {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"stderr-exit", "0", codexTail}},
			"omp":   {Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"stderr-exit", "0", codexTail}},
		},
	}
	s := newServiceFromCfg(t, root, cfg)

	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "codex", Runner: "local",
		Prompt: "count something", Cwd: ".", TimeoutSec: 30,
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Usage == nil {
		t.Fatalf("no usage captured from the codex stderr tail (stderr.log = %q)", readJobLog(t, root, final, "stderr"))
	}
	if final.Usage.TotalTokens != 19802 || final.Usage.Source != runner.UsageSourceCodexStderr {
		t.Fatalf("usage = %+v, want total_tokens 19802 from %s", *final.Usage, runner.UsageSourceCodexStderr)
	}
	if final.Usage.CostUSD != 0 {
		t.Fatalf("cost_usd = %v, want 0 (codex reports no cost)", final.Usage.CostUSD)
	}
	persisted, ok := s.Get(final.ID)
	if !ok || persisted.Usage == nil || persisted.Usage.TotalTokens != 19802 {
		t.Fatalf("persisted usage = %+v (ok=%v), want the captured token count", persisted.Usage, ok)
	}

	other := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "omp", Runner: "local",
		Prompt: "count something", Cwd: ".", TimeoutSec: 30,
	})
	if other.Usage != nil {
		t.Fatalf("usage = %+v, want none: the codex sniff must not read another agent's log", *other.Usage)
	}
}

// readJobLog returns one of a job's captured log streams.
func readJobLog(t *testing.T, root string, res JobResult, stream string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(res.ResultDir, stream+".log"))
	if err != nil {
		t.Fatalf("read %s.log: %v", stream, err)
	}
	return string(b)
}

// TestACPUsageUpdateRecorded: an acp agent reports its usage as a session/update
// event. The runner keeps the LAST recognised update (a run reports a running
// tally), hands it back on the runner result, and the job row persists it — while
// the raw updates stay in acp.jsonl as they always did.
func TestACPUsageUpdateRecorded(t *testing.T) {
	root := t.TempDir()
	s := newACPService(t, root, acptest.Options{UsageUpdate: []string{
		`{"used":1000,"cost":{"total":0.005}}`,
		`{"used":1234,"inputTokens":10,"outputTokens":5,"cost":{"total":0.01}}`,
	}})

	final := acpSubmit(t, s, 30)
	if final.Status != StatusDone {
		t.Fatalf("status = %s (err=%s), want done", final.Status, final.Error)
	}
	if final.Usage == nil {
		t.Fatal("no usage recorded on the acp job")
	}
	if final.Usage.TotalTokens != 1234 || final.Usage.InputTokens != 10 || final.Usage.OutputTokens != 5 || final.Usage.CostUSD != 0.01 {
		t.Fatalf("usage = %+v, want the last usage_update's values", *final.Usage)
	}
	if final.Usage.Source != runner.UsageSourceACP {
		t.Fatalf("usage.source = %q, want %q", final.Usage.Source, runner.UsageSourceACP)
	}

	// The updates are still part of the structured event stream.
	updates := 0
	for _, line := range readACPJSONL(t, final.ResultDir) {
		if line["t"] == "usage_update" {
			updates++
		}
	}
	if updates != 2 {
		t.Fatalf("acp.jsonl carries %d usage_update rows, want the agent's 2", updates)
	}

	persisted, ok := s.Get(final.ID)
	if !ok || persisted.Usage == nil || *persisted.Usage != *final.Usage {
		t.Fatalf("persisted usage = %+v (ok=%v), want %+v", persisted.Usage, ok, final.Usage)
	}
}

// TestUsageSurvivesJobShow: usage is persisted with the job row and read back by
// the detail read — and renders as the one line `job show` prints, including for a
// job that reported only SOME of the counters (a missing counter is omitted, never
// printed as 0).
func TestUsageSurvivesJobShow(t *testing.T) {
	root := t.TempDir()
	s := newServiceFromCfg(t, root, &config.Config{Storage: config.StorageConfig{Root: root}})

	usage := &Usage{
		InputTokens: 12345, OutputTokens: 3800, CacheReadTokens: 289000, TotalTokens: 305145,
		CostUSD: 0.0032, Source: runner.UsageSourceNDJSONOMP,
	}
	snap := JobResult{
		ID: "job-usage-roundtrip", ProjectKey: "self", Agent: "omp", Runner: "local",
		Status: StatusDone, ResultDir: filepath.Join(root, "self", "job-usage-roundtrip"),
		StartedAt: 1_700_000_000, EndedAt: 1_700_000_010, Usage: usage,
	}
	if err := s.persist(snap); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got, ok := s.Get(snap.ID)
	if !ok {
		t.Fatalf("job %s not found in the store", snap.ID)
	}
	if got.Usage == nil {
		t.Fatal("usage did not survive the store round trip")
	}
	if *got.Usage != *usage {
		t.Fatalf("usage = %+v, want %+v", *got.Usage, *usage)
	}
	if want := "in 12.3k / out 3.8k / cache 289k / total 305k / $0.0032 (ndjson:omp)"; FormatUsage(got.Usage) != want {
		t.Fatalf("FormatUsage = %q, want %q", FormatUsage(got.Usage), want)
	}

	// A sparse tally omits what the agent never reported.
	sparse := &Usage{TotalTokens: 19802, Source: runner.UsageSourceCodexStderr}
	if want := "total 19.8k (codex:stderr)"; FormatUsage(sparse) != want {
		t.Fatalf("FormatUsage = %q, want %q", FormatUsage(sparse), want)
	}
	if got := FormatUsage(nil); got != "" {
		t.Fatalf("FormatUsage(nil) = %q, want an empty string (a job with no usage prints no line)", got)
	}
}
