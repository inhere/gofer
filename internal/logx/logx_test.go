package logx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func closeFileSink() {
	setupMu.Lock()
	defer setupMu.Unlock()
	if fileSink != nil {
		_ = fileSink.Close()
		fileSink = nil
	}
	if stderrHandler != nil {
		slog.SetDefault(slog.New(stderrHandler))
	}
}

// TestShortenTimeDropsZone: the time attr is reformatted to timeLayout with no
// zone offset (the trailing "+08:00" the user wanted gone).
func TestShortenTimeDropsZone(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: shortenTime})
	slog.New(h).Info("hello", "k", "v")

	out := buf.String()
	if want := "time=2"; !bytes.Contains(buf.Bytes(), []byte(want)) {
		t.Fatalf("missing time attr in %q", out)
	}
	// No RFC3339 zone offset (+08:00 / -07:00 / Z) on the timestamp.
	zone := regexp.MustCompile(`time=\S*(?:[+-]\d{2}:\d{2}|Z)`)
	if zone.MatchString(out) {
		t.Fatalf("time still carries a zone offset: %q", out)
	}
	// Timestamp matches timeLayout: 2006-01-02T15:04:05.000
	layout := regexp.MustCompile(`time=\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}\b`)
	if !layout.MatchString(out) {
		t.Fatalf("time not in expected layout %q: %q", timeLayout, out)
	}
}

func TestConfigureFileJSONLRedactsAndFansOut(t *testing.T) {
	t.Cleanup(closeFileSink)
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	Setup()
	if err := ConfigureFile(FileOptions{Path: path, Explicit: true, Component: "serve", MaxSizeMB: 1}); err != nil {
		t.Fatal(err)
	}
	slog.Default().Info("hello", "Authorization", "bearer-secret", "nested", slog.GroupValue(slog.String("password", "pw")), "ok", "yes")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(b), &row); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"time", "level", "component", "operation_id", "msg"} {
		if _, ok := row[k]; !ok {
			t.Errorf("missing %s in %s", k, b)
		}
	}
	if row["component"] != "serve" || row["msg"] != "hello" {
		t.Errorf("row=%v", row)
	}
	if strings.Contains(string(b), "bearer-secret") || strings.Contains(string(b), `"pw"`) {
		t.Errorf("secret leaked: %s", b)
	}
	closeFileSink()
}

func TestConfigureFileRotationAndConcurrentWrites(t *testing.T) {
	t.Cleanup(closeFileSink)
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	Setup()
	if err := ConfigureFile(FileOptions{Path: path, Explicit: true, Component: "worker", MaxSizeMB: 1, MaxBackups: 2, MaxAgeDays: 14}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 400; j++ {
				slog.Info("rotation", "value", strings.Repeat("x", 1500))
			}
		}()
	}
	wg.Wait()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	backups := 0
	for _, e := range entries {
		if e.Name() == "run.log" || (strings.HasPrefix(e.Name(), "run-") && strings.HasSuffix(e.Name(), ".log")) {
			found = true
			if e.Name() != "run.log" {
				backups++
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var row map[string]any
				if err := json.Unmarshal(line, &row); err != nil {
					t.Fatalf("invalid JSONL in %s: %v", e.Name(), err)
				}
			}
		}
	}
	if !found {
		t.Fatal("no log files")
	}
	if backups == 0 {
		t.Fatal("rotation did not create a backup")
	}
	if backups > 2 {
		t.Fatalf("rotation retained %d backups, want <= 2", backups)
	}
	closeFileSink()
}

func TestRotationKeepsUnexpiredBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.log")
	recent := filepath.Join(dir, "run-20990101-000000.log")
	if err := os.WriteFile(recent, []byte(`{"msg":"recent"}\n`), 0644); err != nil {
		t.Fatal(err)
	}
	Setup()
	t.Cleanup(closeFileSink)
	if err := ConfigureFile(FileOptions{Path: path, Explicit: true, Component: "test", MaxSizeMB: 1, MaxAgeDays: 14, MaxBackups: 10}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 900; i++ {
		slog.Info("rotate", "value", strings.Repeat("x", 1500))
	}
	closeFileSink()
	if _, err := os.Stat(recent); err != nil {
		t.Fatalf("recent backup was removed: %v", err)
	}
}

func TestConfigureFileExplicitFailureAndImplicitFallback(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	Setup()
	if err := ConfigureFile(FileOptions{Path: filepath.Join(blocked, "run.log"), Explicit: true}); err == nil {
		t.Fatal("explicit failure not returned")
	}
	Setup()
	if err := ConfigureFile(FileOptions{Path: filepath.Join(blocked, "run.log"), Explicit: false}); err != nil {
		t.Fatal(err)
	}
}

func TestMultiHandlerMethods(t *testing.T) {
	var a, b bytes.Buffer
	m := multiHandler{handlers: []slog.Handler{slog.NewTextHandler(&a, nil), slog.NewTextHandler(&b, nil)}}
	l := slog.New(m.WithAttrs([]slog.Attr{slog.String("a", "b")}).WithGroup("g"))
	if !l.Handler().Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("disabled")
	}
	l.Info("x")
	if a.Len() == 0 || b.Len() == 0 {
		t.Fatal("fanout failed")
	}
}

func TestConfigureFileRebuildsFromStderrBase(t *testing.T) {
	t.Cleanup(closeFileSink)
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first.log"), filepath.Join(dir, "second.log")
	Setup()
	if err := ConfigureFile(FileOptions{Path: first, Explicit: true, Component: "first"}); err != nil {
		t.Fatal(err)
	}
	slog.Info("one")
	if err := ConfigureFile(FileOptions{Path: second, Explicit: true, Component: "second"}); err != nil {
		t.Fatal(err)
	}
	slog.Info("two")
	closeFileSink()
	firstData, _ := os.ReadFile(first)
	secondData, _ := os.ReadFile(second)
	if bytes.Count(firstData, []byte("\n")) != 1 || bytes.Count(secondData, []byte("\n")) != 1 {
		t.Fatalf("unexpected records first=%q second=%q", firstData, secondData)
	}
}

// TestParseLevel covers the GOFER_LOG_LEVEL mapping including the default.
func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelInfo,
		"bogus":   slog.LevelInfo,
		" INFO ":  slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevel(in); got != want {
			t.Errorf("parseLevel(%q)=%v, want %v", in, got, want)
		}
	}
}
