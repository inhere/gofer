package envx

import (
	"os"
	"strings"
	"testing"
)

// lastValue returns the value of the LAST KEY=VALUE entry for key — i.e. what
// os/exec's env dedup (last occurrence wins) hands to the child process.
func lastValue(env []string, key string) (string, bool) {
	value, ok := "", false
	for _, kv := range env {
		if k, v, found := strings.Cut(kv, "="); found && k == key {
			value, ok = v, true
		}
	}
	return value, ok
}

// TestEnvironLayersExtraOnTop: extra overrides an inherited key (because it is
// appended last, which is what os/exec dedup keeps) and adds new keys.
func TestEnvironLayersExtraOnTop(t *testing.T) {
	t.Setenv("GOFER_TEST_INHERITED", "from-process")
	got := Environ(map[string]string{
		"GOFER_TEST_INHERITED": "from-extra",
		"GOFER_TEST_ADDED":     "added",
	})
	if v, ok := lastValue(got, "GOFER_TEST_INHERITED"); !ok || v != "from-extra" {
		t.Fatalf("inherited key not overridden: %q (present=%v)", v, ok)
	}
	if v, ok := lastValue(got, "GOFER_TEST_ADDED"); !ok || v != "added" {
		t.Fatalf("extra key missing: %q (present=%v)", v, ok)
	}
	// The inherited entries are still there (extra layers ON TOP, it does not
	// replace the process env).
	if _, ok := lastValue(got, "PATH"); !ok {
		t.Fatalf("process env dropped: PATH missing from %d entries", len(got))
	}
}

// TestEnvironEmptyExtra: an empty/nil extra yields the process env unchanged, so
// a job with no env inherits exactly what the previous mergedEnv copies did.
func TestEnvironEmptyExtra(t *testing.T) {
	for _, extra := range []map[string]string{nil, {}} {
		got := Environ(extra)
		if len(got) != len(os.Environ()) {
			t.Fatalf("Environ(%v) returned %d entries, want %d", extra, len(got), len(os.Environ()))
		}
	}
}

func TestMergeOverrideWinsAndInputsUntouched(t *testing.T) {
	base := map[string]string{"A": "1", "B": "2"}
	extra := map[string]string{"B": "override", "C": "3"}

	got := Merge(base, extra)
	if len(got) != 3 || got["A"] != "1" || got["B"] != "override" || got["C"] != "3" {
		t.Fatalf("Merge = %v, want A=1 B=override C=3", got)
	}
	if len(base) != 2 || base["B"] != "2" {
		t.Fatalf("base mutated: %v", base)
	}
	if len(extra) != 2 || extra["B"] != "override" {
		t.Fatalf("extra mutated: %v", extra)
	}
	// The result is a fresh map (role env must never alias the request env).
	got["A"] = "mutated"
	if base["A"] != "1" {
		t.Fatalf("Merge aliased base: %v", base)
	}
}

// TestMergeEmptyExtraReturnsBase pins the no-copy contract the job pipeline
// relies on (an envless job must not pay for a copy it does not need).
func TestMergeEmptyExtraReturnsBase(t *testing.T) {
	base := map[string]string{"A": "1"}
	if got := Merge(base, nil); got["A"] != "1" {
		t.Fatalf("Merge(base, nil) = %v", got)
	}
	got := Merge(base, nil)
	got["A"] = "mutated"
	if base["A"] != "mutated" {
		t.Fatalf("Merge(base, nil) copied instead of returning base: %v", base)
	}
	if got := Merge(nil, nil); got != nil {
		t.Fatalf("Merge(nil, nil) = %v, want nil", got)
	}
}

// TestWithAlwaysCopies: gofer-owned keys win over a caller-supplied key of the
// same name, and the caller's map stays untouched (the job metadata env seam).
func TestWithAlwaysCopies(t *testing.T) {
	base := map[string]string{"GOFER_JOB_ID": "caller-supplied", "A": "1"}
	got := With(base, map[string]string{"GOFER_JOB_ID": "job-1"})

	if got["GOFER_JOB_ID"] != "job-1" {
		t.Fatalf("gofer-owned key did not win: %v", got)
	}
	if got["A"] != "1" {
		t.Fatalf("base key lost: %v", got)
	}
	if base["GOFER_JOB_ID"] != "caller-supplied" {
		t.Fatalf("base mutated: %v", base)
	}
	got["A"] = "mutated"
	if base["A"] != "1" {
		t.Fatalf("With aliased base: %v", base)
	}
}

func TestWithNilBase(t *testing.T) {
	got := With(nil, map[string]string{"GOFER_CWD": "/work"})
	if len(got) != 1 || got["GOFER_CWD"] != "/work" {
		t.Fatalf("With(nil, ...) = %v", got)
	}
}
