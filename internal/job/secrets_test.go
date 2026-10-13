package job

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/testutil/testcmd"
)

type nopCloseBuffer struct {
	bytes.Buffer
	closed bool
}

func (b *nopCloseBuffer) Close() error { b.closed = true; return nil }

// The masker replaces every value, also one split across writes or written a byte at
// a time, and flushes the held-back tail on Close.
func TestSecretMasker(t *testing.T) {
	const secret = "s3cr3t-token-value"
	for name, chunks := range map[string][]string{
		"one write":    {"before " + secret + " after\n"},
		"split":        {"before s3cr3t-to", "ken-value after\n"},
		"byte by byte": strings.Split("before "+secret+" after\n", ""),
		"twice":        {secret + secret + "\n"},
	} {
		var out nopCloseBuffer
		w := newSecretMasker(&out, []string{"short-other", secret})
		for _, c := range chunks {
			if n, err := w.Write([]byte(c)); err != nil || n != len(c) {
				t.Fatalf("%s: write %d %v", name, n, err)
			}
		}
		if err := w.Close(); err != nil || !out.closed {
			t.Fatalf("%s: close %v closed=%v", name, err, out.closed)
		}
		got := out.String()
		if strings.Contains(got, "s3cr3t") || !strings.Contains(got, secretMaskText) {
			t.Errorf("%s: %q", name, got)
		}
		if name != "twice" && got != "before *** after\n" {
			t.Errorf("%s: %q", name, got)
		}
	}
	// No values: the writer itself, untouched.
	var out nopCloseBuffer
	if w := newSecretMasker(&out, nil); w != &out {
		t.Fatal("a masker without values must be the writer itself")
	}
}

func newSecretService(t *testing.T) *Service {
	t.Helper()
	repo, _ := gitRepo(t)
	s := newWorktreeService(t, repo, t.TempDir())
	cfg := s.cfg.Load()
	cfg.Secrets = map[string]config.SecretConfig{
		"gh-token": {Env: "SECRET_TEST_SRC_X"},
		"other":    {Env: "OTHER_SRC", As: "OTHER_TOKEN"},
	}
	p := cfg.Projects["repo"]
	p.Secrets = []string{"gh-token"}
	cfg.Projects["repo"] = p
	return s
}

// A granted secret reaches the job under its job variable, its value stays out of the
// request and the logs, and the grant is recorded.
func TestJobSecretIsGrantedAndMasked(t *testing.T) {
	const value = "ghp_example_value_0123456789"
	t.Setenv("SECRET_TEST_SRC_X", value)
	s := newSecretService(t)
	j := submitAndWait(t, s, JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		Cmd: []string{testcmd.Path(t), "env-print", "GH_TOKEN"}, Secrets: []string{"gh-token", " gh-token "}})
	if j.Status != StatusDone {
		t.Fatalf("job = %s %s", j.Status, j.Error)
	}
	out := repoLog(t, s.cfg.Load().Storage.Root, j.ID)
	if strings.Contains(out, value) || !strings.Contains(out, "GH_TOKEN=***") {
		t.Fatalf("stdout = %q", out)
	}
	if strings.Contains(j.RequestJSON, value) || !strings.Contains(j.RequestJSON, `"secrets":["gh-token"]`) {
		t.Fatalf("request_json = %s", j.RequestJSON)
	}
	if !hasSubsequence(eventTypes(t, s, j.ID), []string{EventJobSubmitted, EventJobSecretsGranted}) {
		t.Fatalf("events = %v", eventTypes(t, s, j.ID))
	}
}

func TestJobSecretRefusals(t *testing.T) {
	t.Setenv("SECRET_TEST_SRC_X", "")
	s := newSecretService(t)
	base := JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30, Cmd: []string{"git", "status"}}
	for name, tc := range map[string]struct {
		secrets     []string
		interactive bool
		want        string
	}{
		"undeclared":     {[]string{"nope"}, false, "not declared"},
		"not allowed":    {[]string{"other"}, false, "does not allow secret"},
		"variable unset": {[]string{"gh-token"}, false, "SECRET_TEST_SRC_X is not set"},
	} {
		req := base
		req.Secrets, req.Interactive = tc.secrets, tc.interactive
		_, err := s.Submit(req)
		if !errors.Is(err, ErrInvalidRequest) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The shapes that cannot carry a secret are refused before anything runs.
	cfg := s.cfg.Load()
	proj := cfg.Projects["repo"]
	inter := base
	inter.Secrets, inter.Interactive = []string{"gh-token"}, true
	if err := checkSecrets(cfg, proj, inter, false); err == nil || !strings.Contains(err.Error(), "--interactive") {
		t.Errorf("interactive: %v", err)
	}
	remote := base
	remote.Secrets, remote.Runner = []string{"gh-token"}, "w-1"
	if err := checkSecrets(cfg, proj, remote, true); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Errorf("remote: %v", err)
	}
	t.Setenv("SECRET_TEST_SRC_X", "abc")
	if _, err := s.Submit(JobRequest{ProjectKey: "repo", Agent: "exec", Runner: "local", Cwd: ".", TimeoutSec: 30,
		Cmd: []string{"git", "status"}, Secrets: []string{"gh-token"}}); err == nil || !strings.Contains(err.Error(), "shorter than") {
		t.Errorf("short value: %v", err)
	}
}
