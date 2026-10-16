package authz

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestMatch(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"job.read.others", "job.read.others", true},
		{"job.read.others", "job.read.other", false},
		{"job.read", "job.read.others", false},
		{"job.*", "job.read.others", true},
		{"job.*", "job.", true},
		{"job.*", "tunnel.open", false},
		{"*", "anything.at.all", true},
		{"*", "", true}, // `*` matches the empty run as well
		{"", "", true},
		{"", "job.submit", false},
		{"job.*.others", "job.read.others", true},
		{"job.*.others", "job.attach", false},
		{"job.*.others", "job.read.own", false},
		{"job*.others", "job.wake.others", true},
		{"job*.others", "interaction.answer", false},
		{"*.others", "xfer.manage.others", true},
		{"*.others", "xfer.manage", false},
		{"job*", "job.submit", true},
		{"*submit", "job.submit", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "aXc", false},
		// Match is the raw glob: it happily matches auth.manage. Expand is what refuses
		// to hand out auth.manage (see TestExpandNeverMatchesAuthManage).
		{"*", PermAuthManage, true},
		// Only `*` is special: `?` and `[` are literal bytes, not regular expressions.
		{"jo?.*", "job.submit", false},
		{"jo[b].*", "job.submit", false},
	}
	for _, tc := range cases {
		if got := Match(tc.pattern, tc.name); got != tc.want {
			t.Errorf("Match(%q, %q) = %t, want %t", tc.pattern, tc.name, got, tc.want)
		}
	}
}

// TestExpandWildcardNeverReachesDangerous covers design §2「通配符不展开到危险级」: a
// dangerous permission is only ever handed out when the pattern spells its name.
func TestExpandWildcardNeverReachesDangerous(t *testing.T) {
	dangerous := dangerousNames()
	if len(dangerous) == 0 {
		t.Fatal("the catalog has no dangerous permission, so the rule cannot be exercised")
	}
	for _, name := range dangerous {
		// Spelled out literally, a dangerous permission expands to exactly itself.
		if got := expandNames(t, name, SubjectUsers); !slices.Equal(got, []string{name}) {
			t.Errorf("Expand(%q) = %v, want only itself", name, got)
		}
		// Behind a wildcard it is never handed out, e.g. `job.*` must not yield job.submit.
		wildcard := strings.SplitN(name, ".", 2)[0] + ".*"
		e, err := Expand(wildcard, SubjectUsers)
		if err != nil {
			if !errors.Is(err, ErrNoMatch) {
				t.Errorf("Expand(%q): %v", wildcard, err)
			}
			continue // the wildcard reaches nothing but that dangerous permission
		}
		if slices.Contains(e.Names(), name) {
			t.Errorf("Expand(%q) handed out the dangerous permission %s", wildcard, name)
		}
	}
	// A pattern that only reaches a dangerous permission matches nothing at all.
	if _, err := Expand("memory.*", SubjectUsers); !errors.Is(err, ErrNoMatch) {
		t.Errorf("Expand(memory.*) = %v, want ErrNoMatch (memory.write is dangerous)", err)
	}
	// The star hands out every permission except the dangerous and the root ones.
	wantStar := catalogNames(func(p Permission) bool { return p.Risk != RiskDangerous && p.Risk != RiskRoot })
	if got := expandNames(t, "*", SubjectUsers); !slices.Equal(got, wantStar) {
		t.Errorf("Expand(*) = %v, want %v", got, wantStar)
	}
}

// TestExpandNeverMatchesAuthManage covers design §2「任何授权都匹配不到 auth.manage」.
func TestExpandNeverMatchesAuthManage(t *testing.T) {
	// Patterns that could only ever reach auth.manage match nothing at all.
	for _, pattern := range []string{"auth.*", "auth.manage", "auth*", "auth.m*", "a*.manage"} {
		if _, err := Expand(pattern, SubjectUsers); !errors.Is(err, ErrNoMatch) {
			t.Errorf("Expand(%q) = %v, want ErrNoMatch: no grant ever matches %s", pattern, err, PermAuthManage)
		}
	}
	// It is absent from a broad expansion for every subject kind as well.
	for _, subject := range []SubjectKind{SubjectUsers, SubjectCaller, SubjectSession} {
		if names := expandNames(t, "*", subject); slices.Contains(names, PermAuthManage) {
			t.Errorf("Expand(*, %s) handed out %s", subject, PermAuthManage)
		}
		if _, err := Expand(PermAuthManage, subject); !errors.Is(err, ErrNoMatch) {
			t.Errorf("Expand(%s, %s) = %v, want ErrNoMatch", PermAuthManage, subject, err)
		}
	}
}

// TestExpandSessionSubjectOnlyGrantable covers design §2「会话授权只展开到『授予会话 =
// 是』的权限」.
func TestExpandSessionSubjectOnlyGrantable(t *testing.T) {
	want := sessionGrantableNames()
	if len(want) == 0 {
		t.Fatal("the catalog has no session-grantable permission, so the rule cannot be exercised")
	}
	if got := expandNames(t, "*", SubjectSession); !slices.Equal(got, want) {
		t.Errorf("Expand(*, session) = %v, want the session-grantable set %v", got, want)
	}
	// A wildcard keeps only the session-grantable permissions it reaches.
	wantJob := []string{PermJobReadOthers, PermJobCancelOthers, PermJobAttach, PermJobWakeOthers}
	if got := expandNames(t, "job.*", SubjectSession); !slices.Equal(got, wantJob) {
		t.Errorf("Expand(job.*, session) = %v, want %v", got, wantJob)
	}
	// The filter is "grantable to a session", not "low risk": a high-risk permission
	// still expands, and a dangerous one never does.
	if got := expandNames(t, "session.reply.*", SubjectSession); !slices.Equal(got, []string{PermSessionReplyOthers}) {
		t.Errorf("Expand(session.reply.*, session) = %v, want %v", got, []string{PermSessionReplyOthers})
	}
	if got := expandNames(t, PermUpdateApply, SubjectSession); !slices.Equal(got, []string{PermUpdateApply}) {
		t.Errorf("Expand(%s, session) = %v, want only itself", PermUpdateApply, got)
	}
	for _, name := range []string{PermJobSubmit, PermWorkerUpgradeUpload, PermSessionManageOthers, PermSessionTransfer, PermXferWrite, PermMemoryWrite} {
		if _, err := Expand(name, SubjectSession); !errors.Is(err, ErrNoMatch) {
			t.Errorf("Expand(%s, session) = %v, want ErrNoMatch: it may not be granted to a session", name, err)
		}
	}
	// users and caller expand identically, and keep the high-risk permissions.
	if got := expandNames(t, "job.*", SubjectCaller); !slices.Equal(got, expandNames(t, "job.*", SubjectUsers)) {
		t.Errorf("caller expansion = %v, want the users expansion", got)
	}
	if !slices.Contains(expandNames(t, "job.*", SubjectUsers), PermJobManageOthers) {
		t.Errorf("Expand(job.*) must keep the high-risk %s", PermJobManageOthers)
	}
}

// TestExpandRequiresAMatch covers design §2「保存时模式必须至少匹配一项」.
func TestExpandRequiresAMatch(t *testing.T) {
	cases := []struct {
		pattern string
		subject SubjectKind
	}{
		{"", SubjectUsers},
		{"nope.*", SubjectUsers},
		{"job.nonexistent", SubjectCaller},
		{"*.nope", SubjectUsers},
		{"memory.*", SubjectUsers},      // only reaches a dangerous permission
		{PermJobSubmit, SubjectSession}, // dangerous, and not grantable to a session
		{"auth.*", SubjectUsers},        // only reaches auth.manage
	}
	for _, tc := range cases {
		if _, err := Expand(tc.pattern, tc.subject); !errors.Is(err, ErrNoMatch) {
			t.Errorf("Expand(%q, %s) = %v, want ErrNoMatch", tc.pattern, tc.subject, err)
		}
	}
	_, err := Expand("nope.*", SubjectUsers)
	if err == nil || !strings.Contains(err.Error(), "nope.*") {
		t.Errorf("the error must name the pattern, got %v", err)
	}
	// The smallest patterns that do work: one literal name, and a star.
	if got := expandNames(t, PermTrackerSync, SubjectUsers); !slices.Equal(got, []string{PermTrackerSync}) {
		t.Errorf("Expand(%s) = %v, want only itself", PermTrackerSync, got)
	}
	if got := expandNames(t, "*", SubjectUsers); len(got) < 2 {
		t.Errorf("Expand(*) = %v, want the whole non-dangerous catalog", got)
	}
}

func TestExpandRejectsUnknownSubject(t *testing.T) {
	for _, subject := range []SubjectKind{"", "sessions", "worker", "Users"} {
		if _, err := Expand("*", subject); !errors.Is(err, ErrUnknownSubject) {
			t.Errorf("Expand(*, %q) = %v, want ErrUnknownSubject", string(subject), err)
		}
	}
	for _, subject := range []SubjectKind{SubjectUsers, SubjectCaller, SubjectSession} {
		if !subject.Valid() {
			t.Errorf("%s must be a valid subject kind", subject)
		}
		if _, err := Expand(PermConfigRead, subject); err != nil {
			t.Errorf("Expand(%s, %s): %v", PermConfigRead, subject, err)
		}
		if subject.Label() == "" {
			t.Errorf("%s has no label", subject)
		}
	}
	if SubjectKind("worker").Valid() {
		t.Errorf("worker credentials are not a grant subject kind (design §2)")
	}
}

// TestExpansionNamesAndSnapshot covers the shapes an expansion exposes: names in design
// order, and the sorted snapshot a grant stores.
func TestExpansionNamesAndSnapshot(t *testing.T) {
	e := mustExpand(t, "tunnel.*", SubjectUsers)
	want := []string{PermTunnelOpen, PermTunnelHosted, PermTunnelManageOthers}
	if e.Pattern != "tunnel.*" || e.Subject != SubjectUsers {
		t.Errorf("expansion = %q/%s, want tunnel.*/users", e.Pattern, e.Subject)
	}
	if got := e.Names(); !slices.Equal(got, want) {
		t.Errorf("Names() = %v, want design order %v", got, want)
	}
	sorted := []string{PermTunnelHosted, PermTunnelManageOthers, PermTunnelOpen}
	if got := e.Snapshot(); !slices.Equal(got, sorted) {
		t.Errorf("Snapshot() = %v, want %v", got, sorted)
	}
	if !slices.IsSorted(e.Snapshot()) {
		t.Errorf("Snapshot() = %v, want a sorted list", e.Snapshot())
	}
	// Names and Snapshot hand out copies: a caller may sort or edit them freely.
	snap := e.Snapshot()
	snap[0] = "mutated"
	names := e.Names()
	names[0] = "mutated"
	if got := e.Snapshot(); !slices.Equal(got, sorted) {
		t.Errorf("Snapshot() changed to %v after the caller edited a copy", got)
	}
	if got := e.Names(); !slices.Equal(got, want) {
		t.Errorf("Names() changed to %v after the caller edited a copy", got)
	}
}

// TestNewSinceReportsCatalogUpgrades covers the「有新权限待确认」comparison (design §2).
func TestNewSinceReportsCatalogUpgrades(t *testing.T) {
	// A grant saved when `tracker.*` meant tracker.sync only.
	pending, err := NewSince("tracker.*", SubjectUsers, []string{PermTrackerSync})
	if err != nil {
		t.Fatalf("NewSince: %v", err)
	}
	if got := permissionNames(pending); !slices.Equal(got, []string{PermTrackerDelete}) {
		t.Errorf("pending = %v, want [%s]", got, PermTrackerDelete)
	}
	// With the snapshot up to date nothing waits for confirmation.
	pending, err = NewSince("tracker.*", SubjectUsers, []string{PermTrackerSync, PermTrackerDelete})
	if err != nil {
		t.Fatalf("NewSince: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending = %v, want nothing", permissionNames(pending))
	}
	// An empty snapshot reports the whole expansion.
	if got := permissionNames(mustNewSince(t, "tracker.*", SubjectUsers, nil)); !slices.Equal(got, []string{PermTrackerSync, PermTrackerDelete}) {
		t.Errorf("pending = %v, want the whole tracker expansion", got)
	}
	// Only what the grant really means can be new: a wildcard never reaches a dangerous
	// permission, so `xfer.*` reports xfer.manage.others and not xfer.write.
	if got := permissionNames(mustNewSince(t, "xfer.*", SubjectUsers, nil)); !slices.Equal(got, []string{PermXferManageOthers}) {
		t.Errorf("pending = %v, want [%s]: the wildcard does not mean the dangerous %s", got, PermXferManageOthers, PermXferWrite)
	}
	// Same for a session grant: tunnel.hosted is not grantable to a session, so it is
	// not something the session grant grows into.
	if got := permissionNames(mustNewSince(t, "tunnel.*", SubjectSession, nil)); !slices.Equal(got, []string{PermTunnelOpen}) {
		t.Errorf("pending = %v, want [%s]", got, PermTunnelOpen)
	}
	// The errors of Expand travel through unchanged.
	if _, err := NewSince("tunnel.*", SubjectKind("worker"), nil); !errors.Is(err, ErrUnknownSubject) {
		t.Errorf("NewSince(worker) = %v, want ErrUnknownSubject", err)
	}
	if _, err := NewSince("nope.*", SubjectUsers, nil); !errors.Is(err, ErrNoMatch) {
		t.Errorf("NewSince(nope.*) = %v, want ErrNoMatch", err)
	}
}

// TestExpansionAgainstAGrownCatalog exercises the upgrade the snapshot exists for: a
// permission added to the catalog after a grant was saved. A grant stored before the
// row existed keeps its old meaning (the new row is reported, not granted silently), and
// a new DANGEROUS row never enters a wildcard grant at all.
func TestExpansionAgainstAGrownCatalog(t *testing.T) {
	grown := []Permission{
		{Name: "tracker.sync", Risk: RiskLow, Source: SourceSeed, SeedVersion: SeedV1},
		{Name: "tracker.export", Risk: RiskMedium, Source: SourceSeed, SeedVersion: SeedV1 + 1},
		{Name: "tracker.wipe", Risk: RiskDangerous, Source: SourceConfig},
	}
	perms, err := expandAgainst(grown, "tracker.*", SubjectUsers)
	if err != nil {
		t.Fatalf("expandAgainst: %v", err)
	}
	e := Expansion{Pattern: "tracker.*", Subject: SubjectUsers, Permissions: perms}
	if got := e.Names(); !slices.Equal(got, []string{"tracker.sync", "tracker.export"}) {
		t.Errorf("grown catalog expansion = %v, want tracker.sync and tracker.export only", got)
	}
	if !slices.Equal(e.Snapshot(), []string{"tracker.export", "tracker.sync"}) {
		t.Errorf("snapshot = %v, want the sorted names", e.Snapshot())
	}
	// Nothing in the grown catalog may be granted to a session.
	if _, err := expandAgainst(grown, "tracker.*", SubjectSession); !errors.Is(err, ErrNoMatch) {
		t.Errorf("session expansion = %v, want ErrNoMatch", err)
	}
}

// TestSubjectRoundTrip covers the stored spelling of a grant subject (design §2): the three
// kinds the model has round-trip, and everything else is refused rather than read as one of
// them.
func TestSubjectRoundTrip(t *testing.T) {
	cases := []struct {
		subject string
		kind    SubjectKind
		id      string
	}{
		{subject: "users", kind: SubjectUsers},
		{subject: "caller:alice", kind: SubjectCaller, id: "alice"},
		{subject: "caller:agent-dev", kind: SubjectCaller, id: "agent-dev"},
		{subject: "session:abc-123", kind: SubjectSession, id: "abc-123"},
	}
	for _, tc := range cases {
		kind, id, err := ParseSubject(tc.subject)
		if err != nil {
			t.Errorf("ParseSubject(%q): %v", tc.subject, err)
			continue
		}
		if kind != tc.kind || id != tc.id {
			t.Errorf("ParseSubject(%q) = %s/%q, want %s/%q", tc.subject, kind, id, tc.kind, tc.id)
		}
		if got := SubjectOf(kind, id); got != tc.subject {
			t.Errorf("SubjectOf(%s, %q) = %q, want %q", kind, id, got, tc.subject)
		}
	}
	for _, bad := range []string{"", "users:", "caller:", "caller: ", "session:", "worker:w1", "job:j1", "alice", "caller"} {
		if _, _, err := ParseSubject(bad); !errors.Is(err, ErrUnknownSubject) {
			t.Errorf("ParseSubject(%q) = %v, want ErrUnknownSubject", bad, err)
		}
	}
	if got := SubjectOf(SubjectKind("worker"), "w1"); got != "" {
		t.Errorf("SubjectOf(worker) = %q, want empty", got)
	}
}

func mustExpand(t *testing.T, pattern string, subject SubjectKind) Expansion {
	t.Helper()
	e, err := Expand(pattern, subject)
	if err != nil {
		t.Fatalf("Expand(%q, %s): %v", pattern, subject, err)
	}
	return e
}

func expandNames(t *testing.T, pattern string, subject SubjectKind) []string {
	t.Helper()
	return mustExpand(t, pattern, subject).Names()
}

func mustNewSince(t *testing.T, pattern string, subject SubjectKind, snapshot []string) []Permission {
	t.Helper()
	pending, err := NewSince(pattern, subject, snapshot)
	if err != nil {
		t.Fatalf("NewSince(%q, %s): %v", pattern, subject, err)
	}
	return pending
}

func permissionNames(perms []Permission) []string {
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		out = append(out, p.Name)
	}
	return out
}
