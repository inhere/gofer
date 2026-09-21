package acp

import (
	"bytes"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/runner/ndjsonfilter"
)

// TestStderrEventLineShapeMatchesNdjson pins the stderr projection (bd h-aii-rnxk): a
// tool_call/thought/permission/plan/stop line the acp runner writes to the job's stderr
// is byte-for-byte the compact line shape the ndjson capture writes — which is what the
// web's NdjsonTimeline already parses — including the 2KB cap on an oversized value.
func TestStderrEventLineShapeMatchesNdjson(t *testing.T) {
	long := strings.Repeat("thinking very hard ", 400)

	got := compactLine("thought", func(e *ndjsonfilter.CompactEvent) { e.Add("text", long) })
	want := ndjsonfilter.NewCompactEvent("thought").Add("text", long).Line()
	if !bytes.Equal(got, want) {
		t.Fatalf("stderr event line = %s, want the ndjsonfilter shape %s", got, want)
	}
	if !bytes.HasPrefix(got, []byte(`{"type":"thought","text":`)) {
		t.Fatalf("stderr event line = %s, want `type` first", got)
	}
	if len(got) > ndjsonfilter.DefaultMaxEventBytes {
		t.Fatalf("stderr event line = %d bytes, want <= %d", len(got), ndjsonfilter.DefaultMaxEventBytes)
	}
	if !strings.Contains(string(got), "…(truncated)") {
		t.Fatalf("oversized value was not marked truncated: %d bytes, %s", len(got), got)
	}
}
