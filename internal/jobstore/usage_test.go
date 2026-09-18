package jobstore

import (
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// TestJobUsageRoundTrip: the usage capture (SUP-01 E) is persisted verbatim in
// jobs.usage_json and read back by the job detail read — a job that reported no
// usage reads back as empty (never as an invented zero tally), and a later write
// (a re-run capture) replaces the blob.
func TestJobUsageRoundTrip(t *testing.T) {
	s := openTest(t)

	rec := sampleJob("u1", "proj", 1_700_000_000)
	rec.UsageJSON = `{"input_tokens":12345,"output_tokens":3800,"cache_read_tokens":289000,"total_tokens":305145,"cost_usd":0.0032,"source":"ndjson:omp"}`
	assert.NoErr(t, s.UpsertJob(rec))
	got, ok, err := s.GetJob("u1")
	assert.NoErr(t, err)
	if !ok {
		t.Fatal("job u1 not found")
	}
	if got.UsageJSON != rec.UsageJSON {
		t.Fatalf("usage_json = %q, want %q", got.UsageJSON, rec.UsageJSON)
	}

	plain := sampleJob("u2", "proj", 1_700_000_001)
	assert.NoErr(t, s.UpsertJob(plain))
	got2, ok, err := s.GetJob("u2")
	assert.NoErr(t, err)
	if !ok {
		t.Fatal("job u2 not found")
	}
	if got2.UsageJSON != "" {
		t.Fatalf("usage_json = %q for a job that reported none, want empty", got2.UsageJSON)
	}

	rec.UsageJSON = `{"total_tokens":1,"source":"acp:usage_update"}`
	assert.NoErr(t, s.UpsertJob(rec))
	got3, ok, err := s.GetJob("u1")
	assert.NoErr(t, err)
	if !ok || got3.UsageJSON != rec.UsageJSON {
		t.Fatalf("usage_json = %q after a second write, want %q", got3.UsageJSON, rec.UsageJSON)
	}
}
