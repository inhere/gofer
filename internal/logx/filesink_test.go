package logx

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenFileSinkWritesOnlyItsOwnFile: a FileSink is a component log that must
// not touch the process default logger — the CLI's stdout/stderr (and every
// golden test of it) stays byte-identical while the sink still gets the records,
// with the shared redaction and level handling.
func TestOpenFileSinkWritesOnlyItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run", "client.log")
	sink, err := OpenFileSink(FileOptions{Path: path, Component: "client"})
	if err != nil {
		t.Fatalf("OpenFileSink: %v", err)
	}
	t.Cleanup(func() {
		if err := sink.Close(); err != nil {
			t.Errorf("close sink: %v", err)
		}
	})

	var stderr bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&stderr, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	sink.Logger().Warn("client request failed", "event", "client.request_failed",
		"path", "/v1/jobs", "authorization", "Bearer secret-token")

	if stderr.Len() != 0 {
		t.Fatalf("component sink wrote to the default logger: %q", stderr.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sink file: %v", err)
	}
	if !bytes.Contains(raw, []byte(`"event":"client.request_failed"`)) {
		t.Fatalf("event missing from %q", raw)
	}
	if !bytes.Contains(raw, []byte(`"component":"client"`)) {
		t.Fatalf("component missing from %q", raw)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &row); err != nil {
		t.Fatalf("not JSONL: %v (%q)", err, raw)
	}
	if row["authorization"] != "***" {
		t.Fatalf("sensitive attr not redacted: %v", row["authorization"])
	}
	if row["time"] == nil {
		t.Fatalf("time missing from %q", raw)
	}
}

// TestOpenFileSinkHonoursLogLevel: the sink takes its level from the same
// GOFER_LOG_LEVEL as the default logger, so GOFER_LOG_LEVEL=debug is what makes
// a component's debug lines land in the file.
func TestOpenFileSinkHonoursLogLevel(t *testing.T) {
	t.Setenv("GOFER_LOG_LEVEL", "debug")
	path := filepath.Join(t.TempDir(), "client.log")
	sink, err := OpenFileSink(FileOptions{Path: path, Component: "client"})
	if err != nil {
		t.Fatalf("OpenFileSink: %v", err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	sink.Logger().Debug("debug line", "event", "client.request_succeeded")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sink file: %v", err)
	}
	if !bytes.Contains(raw, []byte("client.request_succeeded")) {
		t.Fatalf("debug line not written: %q", raw)
	}
}

// TestOpenFileSinkUnwritablePath: an impossible path is an error the caller can
// report and move on from (the client treats it as "log disabled").
func TestOpenFileSinkUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenFileSink(FileOptions{Path: filepath.Join(blocked, "client.log")}); err == nil {
		t.Fatal("want an error for a path under a regular file")
	}
}
