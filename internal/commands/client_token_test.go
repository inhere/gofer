package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
)

// TestCLIIgnoresDotenvTokenInsideJob is the other half of F10 leak 1. The CLI
// auto-loads <config-dir>/.env before anything reads the environment, and inside a job
// that config dir is the SERVER's — so the file holds the operator's own GOFER_TOKEN.
// A `gofer` that reads it (the v0.60 field machine had 0.53.1 on PATH, which knows
// nothing about GOFER_JOB_TOKEN) then authenticates as the human and walks straight
// past the credential gate. So when the process already carries GOFER_JOB_TOKEN, the
// dotenv load skips every *_TOKEN key: the credential in force is the job's own, and
// the server's never enters the process environment. Everything else in the file still
// loads — it is the deployment's settings file, not only a credential store.
func TestCLIIgnoresDotenvTokenInsideJob(t *testing.T) {
	cfgDir := t.TempDir()
	content := "GOFER_TOKEN=must-not-load\n" +
		"GOFER_SERVER_TOKEN=must-not-load-either\n" +
		"GOFER_WORKER_TOKEN=must-not-load-third\n" +
		"GOFER_SMOKE_OTHER=kept\n"
	if err := os.WriteFile(filepath.Join(cfgDir, config.EnvFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv(config.EnvConfigDir, cfgDir)
	t.Setenv(job.EnvJobToken, "gjt_job_1_abcdef")
	t.Chdir(t.TempDir()) // no ./.env: the config dir is the only source
	// The three token keys must be ABSENT afterwards, so undo whatever the load did
	// before the restoring t.Setenv cleanups run (LIFO).
	t.Cleanup(func() {
		for _, k := range []string{"GOFER_TOKEN", "GOFER_SERVER_TOKEN", "GOFER_WORKER_TOKEN", "GOFER_SMOKE_OTHER"} {
			_ = os.Unsetenv(k)
		}
	})

	if _, err := config.LoadDotenv(); err != nil {
		t.Fatalf("LoadDotenv: %v", err)
	}

	// The CLI's effective credential is the job's, not the file's.
	if got := resolveClientToken(&config.ServerConfig{}, ""); got != "gjt_job_1_abcdef" {
		t.Fatalf("CLI token inside a job = %q, want the job credential", got)
	}
	// And the server's tokens never entered the environment at all.
	for _, k := range []string{"GOFER_TOKEN", "GOFER_SERVER_TOKEN", "GOFER_WORKER_TOKEN"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Fatalf("%s was loaded from .env inside a job (value %q)", k, v)
		}
	}
	if got := os.Getenv("GOFER_SMOKE_OTHER"); got != "kept" {
		t.Fatalf("GOFER_SMOKE_OTHER = %q, want %q — non-token keys must still load", got, "kept")
	}
}

// TestCLIUsesJobTokenInsideJob is the SEC-01 CLI half: inside a job the client's
// default bearer token is the job's OWN credential (GOFER_JOB_TOKEN), because the
// server token is no longer in the environment at all — a `gofer job comment` typed by
// an agent must authenticate as that job, not fall back to "no token". An explicit
// --token still wins, and outside a job the previous chain is untouched.
func TestCLIUsesJobTokenInsideJob(t *testing.T) {
	sc := &config.ServerConfig{Token: "tok-config"}

	t.Setenv(job.EnvJobToken, "gjt_job_1_abcdef")
	t.Setenv("GOFER_SERVER_TOKEN", "tok-server")
	if got := resolveClientToken(sc, ""); got != "gjt_job_1_abcdef" {
		t.Fatalf("token inside a job = %q, want the job credential", got)
	}
	// An explicit --token still overrides: a human (or a script that must act as a user)
	// can deliberately step out of the job's identity.
	if got := resolveClientToken(sc, "tok-flag"); got != "tok-flag" {
		t.Fatalf("explicit --token = %q, want tok-flag", got)
	}

	// Outside a job nothing changed: env, then config.
	t.Setenv(job.EnvJobToken, "")
	if got := resolveClientToken(sc, ""); got != "tok-server" {
		t.Fatalf("token outside a job = %q, want GOFER_SERVER_TOKEN", got)
	}
	t.Setenv("GOFER_SERVER_TOKEN", "")
	if got := resolveClientToken(sc, ""); got != "tok-config" {
		t.Fatalf("token fallback = %q, want the config token", got)
	}
}
