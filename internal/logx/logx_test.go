package logx

import (
	"bytes"
	"log/slog"
	"regexp"
	"testing"
)

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
