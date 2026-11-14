package job

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
	"github.com/inhere/gofer/internal/store"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

const rawMaskSecret = "ghp_example_raw_value_0123456789"

// TestJobNdjsonRawSidecarMasksGrantedSecrets: with ndjson_raw on, the verbatim
// stdout.raw.log carries the same masking as the logs (a granted value never lands on
// disk in clear) and is created owner-only.
func TestJobNdjsonRawSidecarMasksGrantedSecrets(t *testing.T) {
	t.Setenv("RAW_MASK_SRC", rawMaskSecret)
	root := t.TempDir()
	sample := []string{
		`{"type":"session","id":"` + ndjsonSampleSessionID + `","model":"omp-1","cwd":"x"}`,
		`{"type":"message_update","seq":1,"message":{"role":"assistant","content":[{"type":"text","text":"token ` + rawMaskSecret + ` here"}]}}`,
		`{"type":"message_end","turn":1,"message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
		`{"type":"agent_end","turns":1,"reason":"completed"}`,
	}
	samplePath := filepath.Join(root, "omp-sample.ndjson")
	if err := os.WriteFile(samplePath, []byte(strings.Join(sample, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Storage: config.StorageConfig{Root: root},
		Secrets: map[string]config.SecretConfig{"gh-token": {Env: "RAW_MASK_SRC"}},
		Projects: map[string]config.ProjectConfig{
			"self": {HostPath: root, AllowedAgents: []string{"omp"}, AllowedRunners: []string{"local"}, Secrets: []string{"gh-token"}},
		},
		Agents: map[string]config.AgentConfig{
			"omp": {
				Type: agent.TypeCLIAgent, Command: testcmd.Path(t), Args: []string{"cat-file", samplePath},
				OutputFormat: config.OutputFormatNDJSON, NDJSONRaw: true,
			},
		},
	}
	s := newServiceFromCfg(t, root, cfg)
	final := submitAndWait(t, s, JobRequest{
		ProjectKey: "self", Agent: "omp", Runner: "local", Prompt: "go", Cwd: ".", TimeoutSec: 30,
		Secrets: []string{"gh-token"},
	})
	if final.Status != StatusDone {
		t.Fatalf("status = %s (%s)", final.Status, final.Error)
	}
	rawPath := filepath.Join(final.ResultDir, store.StdoutRawFile)
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("read raw sidecar: %v", err)
	}
	if strings.Contains(string(raw), rawMaskSecret) {
		t.Fatalf("granted secret in stdout.raw.log:\n%s", raw)
	}
	if !strings.Contains(string(raw), "token *** here") || !strings.Contains(string(raw), `"agent_end"`) {
		t.Fatalf("raw sidecar lost its verbatim content:\n%s", raw)
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(rawPath)
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("raw sidecar mode = %v err=%v, want 0600", fi.Mode().Perm(), err)
		}
	}
}

// TestNdjsonRawMaskerMasksValueSplitAcrossWrites: the raw sidecar writer is the
// held-back-tail masker, so a value split across two writes of the child's stdout is
// still masked, and Close flushes what was held back.
func TestNdjsonRawMaskerMasksValueSplitAcrossWrites(t *testing.T) {
	var out nopCloseBuffer
	raw := newSecretMasker(&out, []string{rawMaskSecret})
	var stdout, stderr bytes.Buffer
	f := ndjsonfilter.New(&stdout, &stderr, ndjsonfilter.Options{Raw: raw})
	line := `{"type":"message_update","message":{"text":"x ` + rawMaskSecret + ` y"}}` + "\n"
	cut := strings.Index(line, rawMaskSecret) + 9
	for _, part := range []string{line[:cut], line[cut:]} {
		if _, err := f.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Contains(got, "ghp_") || !strings.Contains(got, "x *** y") {
		t.Fatalf("raw = %q", got)
	}
}
