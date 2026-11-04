package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gookit/goutil/x/assert"
)

// storedRow is a valid stored-caller input; individual tests break one field at a time.
func storedRow(id string) StoredCallerInput {
	return StoredCallerInput{
		ID: id, Name: "CI " + id, TokenHash: strings.Repeat("ab", 32), TokenHint: "gct_…beef",
		CeilingYAML: "projects: [app-*]\n", LimitsJSON: `{"max_concurrent_jobs":2}`,
	}
}

// TestApplyStoredCallersAppendsFileCallersUntouched: a stored caller is appended after the
// file callers (the file order is never touched), carries its source mark and can never be
// a root administrator.
func TestApplyStoredCallersAppendsFileCallersUntouched(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Callers: []CallerConfig{{ID: "operator", CanAdmin: true}}}}
	res, err := ApplyStoredCallers(cfg, []StoredCallerInput{storedRow("ci")})
	assert.NoErr(t, err)
	assert.Len(t, res.Superseded, 0)
	assert.Len(t, cfg.Server.Callers, 2)
	assert.Eq(t, "operator", cfg.Server.Callers[0].ID)
	assert.Eq(t, "", cfg.Server.Callers[0].Source)
	merged := cfg.Server.Callers[1]
	assert.Eq(t, "ci", merged.ID)
	assert.Eq(t, CallerSourceDB, merged.Source)
	assert.False(t, merged.CanAdmin, "a stored caller can never be a root administrator")
	assert.Eq(t, 2, merged.MaxConcurrentJobs)
	if merged.Ceiling == nil || len(merged.Ceiling.Projects) != 1 || merged.Ceiling.Projects[0] != "app-*" {
		t.Fatalf("ceiling = %+v, want projects [app-*]", merged.Ceiling)
	}
}

// TestApplyStoredCallersRejectsUnusableRows (D17): every row that would make the merged
// configuration unacceptable is an error that names the caller — the server must not start
// on a caller set it cannot validate, and a repair command has an id to point at.
func TestApplyStoredCallersRejectsUnusableRows(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(r *StoredCallerInput)
		want   string
	}{
		{"short hash", func(r *StoredCallerInput) { r.TokenHash = "deadbeef" }, "token hash"},
		{"non hex hash", func(r *StoredCallerInput) { r.TokenHash = strings.Repeat("zz", 32) }, "token hash"},
		{"reserved id", func(r *StoredCallerInput) { r.ID = "default" }, "reserved"},
		{"job prefix", func(r *StoredCallerInput) { r.ID = "job-1" }, "job credentials"},
		{"bad glob", func(r *StoredCallerInput) { r.CeilingYAML = "projects: [\"[\"]" }, "invalid pattern"},
		{"empty ceiling", func(r *StoredCallerInput) { r.CeilingYAML = "{}" }, "ceiling is empty"},
		{"unknown ceiling key", func(r *StoredCallerInput) { r.CeilingYAML = "projecs: [a]" }, "ceiling"},
		{"negative quota", func(r *StoredCallerInput) { r.LimitsJSON = `{"max_concurrent_jobs":-1}` }, "must not be negative"},
		{"broken quota json", func(r *StoredCallerInput) { r.LimitsJSON = `{` }, "not valid JSON"},
		{"control char name", func(r *StoredCallerInput) { r.Name = "ci\x00" }, "control character"},
		{"long name", func(r *StoredCallerInput) { r.Name = strings.Repeat("x", MaxCallerNameLen+1) }, "longer than"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := storedRow("ci")
			tc.mutate(&row)
			cfg := &Config{}
			_, err := ApplyStoredCallers(cfg, []StoredCallerInput{row})
			assert.Err(t, err)
			assert.Contains(t, err.Error(), tc.want)
			assert.Len(t, cfg.Server.Callers, 0, "a rejected row must not be appended")
		})
	}
}

// TestApplyStoredCallersDropsUnusableDisabledRow: a disabled row with an unusable id can
// only come from a hand-edited database, cannot own any job, so it is dropped with a
// warning instead of stopping the server — and it does NOT block its id.
func TestApplyStoredCallersDropsUnusableDisabledRow(t *testing.T) {
	row := storedRow("default")
	row.DisabledAt = 100
	cfg := &Config{}
	res, err := ApplyStoredCallers(cfg, []StoredCallerInput{row})
	assert.NoErr(t, err)
	assert.Len(t, cfg.Server.Callers, 0, "a disabled row with a reserved id is dropped")
	assert.Len(t, res.Warnings, 1, "a dropped disabled row must be reported")

	other := storedRow("job-x")
	other.DisabledAt = 100
	other.ID = "job-x"
	dropped := &Config{}
	_, err = ApplyStoredCallers(dropped, []StoredCallerInput{other})
	assert.NoErr(t, err)
	assert.False(t, dropped.OwnerBlocked("job-x"), "a dropped row must not block its id")
}

// TestApplyStoredCallersFlagsDisabledWarnings: a disabled row with a usable id IS appended
// (its ceiling keeps bounding the jobs it already started) and is reported as a warning;
// a disabled row whose id is unusable is dropped instead (see the previous test), because
// such a row can only come from a hand-edited database and cannot own any job.
func TestApplyStoredCallersFlagsDisabledWarnings(t *testing.T) {
	row := storedRow("ci")
	row.DisabledAt = 100
	cfg := &Config{}
	_, err := ApplyStoredCallers(cfg, []StoredCallerInput{row})
	assert.NoErr(t, err)
	assert.Len(t, cfg.Server.Callers, 1)
	assert.True(t, cfg.Server.Callers[0].Disabled)
	dropped := storedRow("job-y")
	dropped.DisabledAt = 100
	dropped.ID = "job-y"
	res, err := ApplyStoredCallers(&Config{}, []StoredCallerInput{dropped})
	assert.NoErr(t, err)
	assert.Len(t, res.Warnings, 1)
}

// TestApplyStoredCallersHidesSupersededIDs (D10): a stored row whose id the file or a
// worker already has is NOT appended — it is reported for the takeover, and a worker-id
// collision also blocks that owner.
func TestApplyStoredCallersHidesSupersededIDs(t *testing.T) {
	cfg := &Config{Server: ServerConfig{
		Callers: []CallerConfig{{ID: "ci", Token: "file-token"}},
		Workers: map[string]WorkerAuthConfig{"w1": {Token: "worker-token"}},
	}}
	res, err := ApplyStoredCallers(cfg, []StoredCallerInput{storedRow("ci"), storedRow("w1")})
	assert.NoErr(t, err)
	assert.Len(t, res.Superseded, 2)
	assert.Len(t, cfg.Server.Callers, 1, "only the file caller stays in the snapshot")
	assert.Eq(t, "ci", cfg.Server.Callers[0].ID)
	assert.False(t, cfg.OwnerBlocked("ci"), "a file caller is a declared identity, not a blocked owner")
	assert.True(t, cfg.OwnerBlocked("w1"), "a worker id collision must block that owner")
}

// TestApplyStoredCallersClearsDuplicateName: two callers may not share a display name
// (case-insensitive). The stored row keeps its identity and loses the name.
func TestApplyStoredCallersClearsDuplicateName(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Callers: []CallerConfig{{ID: "operator", Name: "CI"}}}}
	row := storedRow("ci")
	row.Name = "ci"
	res, err := ApplyStoredCallers(cfg, []StoredCallerInput{row})
	assert.NoErr(t, err)
	assert.Len(t, cfg.Server.Callers, 2)
	assert.Eq(t, "", cfg.Server.Callers[1].Name)
	assert.Len(t, res.Warnings, 1)
}

// TestOwnerBlockedFollowsDisabled: a disabled stored caller blocks its owner (its queued
// work must not start), an enabled one does not.
func TestOwnerBlockedFollowsDisabled(t *testing.T) {
	cfg := &Config{}
	_, err := ApplyStoredCallers(cfg, []StoredCallerInput{storedRow("ci")})
	assert.NoErr(t, err)
	assert.False(t, cfg.OwnerBlocked("ci"))
	assert.False(t, cfg.OwnerBlocked("nobody"))

	disabled := storedRow("ci2")
	disabled.DisabledAt = 10
	cfg2 := &Config{}
	_, err = ApplyStoredCallers(cfg2, []StoredCallerInput{disabled})
	assert.NoErr(t, err)
	assert.True(t, cfg2.OwnerBlocked("ci2"))
}

// TestMergedCallersAlwaysValidate (D17): whatever the merge accepts must be a configuration
// the loader accepts too, otherwise every later write (and the reload) would fail.
func TestMergedCallersAlwaysValidate(t *testing.T) {
	rows := []StoredCallerInput{
		storedRow("a"),
		{ID: "b", Name: "B", TokenHash: strings.Repeat("cd", 32), LimitsJSON: `{"rate_limit":1.5,"rate_burst":3}`},
		{ID: "c", DisabledAt: 5, TokenHint: "gct_…dead"},
		{ID: "d", TokenHash: strings.Repeat("ef", 32), CeilingYAML: "runners: [w1]\nallow_exec: true\n"},
	}
	cfg := &Config{}
	_, err := ApplyStoredCallers(cfg, rows)
	assert.NoErr(t, err)
	for _, row := range cfg.Server.Callers {
		// A root administrator may not carry a ceiling; a stored caller never is one.
		assert.False(t, row.CanAdmin, "stored caller %q must not be root", row.ID)
	}
	assert.NoErr(t, Validate(cfg))

	// The same merge on a config that declares a caller must validate as well.
	cfg2 := &Config{Server: ServerConfig{Callers: []CallerConfig{{ID: "operator", Token: "t", CanAdmin: true}}}}
	_, err = ApplyStoredCallers(cfg2, rows)
	assert.NoErr(t, err)
	assert.NoErr(t, Validate(cfg2))
}

// TestSaveKeepsFileBytesWithStoredCallers: a save renders ONLY the file callers, so merging
// a stored caller never rewrites the operator's bytes — for a config whose callers were
// nil and for one that declares them (T2's byte assertion).
func TestSaveKeepsFileBytesWithStoredCallers(t *testing.T) {
	for _, fileCallers := range [][]CallerConfig{
		nil,
		{{ID: "operator", Name: "op", Token: "file-token"}},
	} {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		cfg := &Config{Server: ServerConfig{Addr: "127.0.0.1:1", Callers: fileCallers}}
		assert.NoErr(t, Save(path, cfg))
		before, err := os.ReadFile(path)
		assert.NoErr(t, err)

		loaded, _, err := Load(path)
		assert.NoErr(t, err)
		_, err = ApplyStoredCallers(loaded, []StoredCallerInput{storedRow("ci")})
		assert.NoErr(t, err)
		assert.Len(t, loaded.Server.Callers, len(fileCallers)+1)
		assert.NoErr(t, Save(path, loaded))

		after, err := os.ReadFile(path)
		assert.NoErr(t, err)
		assert.Eq(t, string(before), string(after), "a save with a stored caller must not change the file")
	}
}

// TestCanonicalConfigKeepsStoredCallers: the reload diff must still see the merged callers
// (only the SAVE path projects them out).
func TestCanonicalConfigKeepsStoredCallers(t *testing.T) {
	cfg := &Config{}
	_, err := ApplyStoredCallers(cfg, []StoredCallerInput{storedRow("ci")})
	assert.NoErr(t, err)
	assert.Len(t, CanonicalConfig(cfg).Server.Callers, 1)
}

// TestHasRootAdminCountsFileCallersOnly: only the FILE decides — a deployment whose callers
// all live in the database keeps the implicit single-token root of a deployment that
// declares none.
func TestHasRootAdminCountsFileCallersOnly(t *testing.T) {
	cfg := &Config{}
	_, err := ApplyStoredCallers(cfg, []StoredCallerInput{storedRow("ci")})
	assert.NoErr(t, err)
	assert.True(t, cfg.Server.HasRootAdmin(), "a stored caller must not replace the implicit default root")
	assert.Len(t, cfg.Server.RootAdminCallers(), 0)

	empty := &Config{Server: ServerConfig{AllowEmptyToken: true}}
	assert.False(t, empty.Server.HasRootAdmin())

	declared := &Config{Server: ServerConfig{Callers: []CallerConfig{{ID: "op", CanAdmin: true}}}}
	_, err = ApplyStoredCallers(declared, []StoredCallerInput{storedRow("ci")})
	assert.NoErr(t, err)
	assert.True(t, declared.Server.HasRootAdmin())
	assert.Eq(t, "op", declared.Server.RootAdminCallers()[0])
}

// TestHasStoreMarker: the marker is asked of the KEY as written, in the main file or in an
// `include` fragment, and a config that never had it answers false.
func TestHasStoreMarker(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "plain.yaml")
	assert.NoErr(t, os.WriteFile(plain, []byte("server:\n  addr: 127.0.0.1:1\n"), 0o600))
	cfg, _, err := Load(plain)
	assert.NoErr(t, err)
	assert.False(t, cfg.HasStoreMarker())

	marked := filepath.Join(dir, "marked.yaml")
	assert.NoErr(t, os.WriteFile(marked, []byte("config_store:\n  id: 1234\nserver:\n  addr: 127.0.0.1:2\n"), 0o600))
	cfg, _, err = Load(marked)
	assert.NoErr(t, err)
	assert.True(t, cfg.HasStoreMarker())

	frag := filepath.Join(dir, "store.yaml")
	assert.NoErr(t, os.WriteFile(frag, []byte("config_store:\n  id: 9999\n"), 0o600))
	split := filepath.Join(dir, "split.yaml")
	assert.NoErr(t, os.WriteFile(split, []byte("include: [store.yaml]\nserver:\n  addr: 127.0.0.1:3\n"), 0o600))
	cfg, _, err = Load(split)
	assert.NoErr(t, err)
	assert.True(t, cfg.HasStoreMarker(), "an include fragment carrying the marker counts")
}

// TestRegisterWorkerRejectsCallerID: a worker id must not collide with a caller id
// (including a stored one).
func TestRegisterWorkerRejectsCallerID(t *testing.T) {
	cfg := &Config{}
	_, err := ApplyStoredCallers(cfg, []StoredCallerInput{storedRow("ci")})
	assert.NoErr(t, err)
	err = RegisterWorkerInto(cfg, "ci", nil, nil, "gw_token")
	assert.Err(t, err)
	assert.Contains(t, err.Error(), "already a caller id")
	assert.NoErr(t, RegisterWorkerInto(cfg, "w1", nil, nil, "gw_token"))
}

// TestValidateTriggersNamesTheFileRule (D1): an owner that is not a FILE caller is refused
// with an error that says why, so an operator who reads it does not try the database.
func TestValidateTriggersNamesTheFileRule(t *testing.T) {
	cfg := &Config{Server: ServerConfig{Triggers: []TriggerConfig{{
		Name: "t", OwnerCaller: "ci", Project: "app",
	}}}}
	_, err := ApplyStoredCallers(cfg, []StoredCallerInput{storedRow("ci")})
	assert.NoErr(t, err)
	err = validateTriggers(cfg)
	assert.Err(t, err)
	assert.Contains(t, err.Error(), "configuration file")
}

// TestDecodeCallerCeilingAndLockedCeiling: the strict decoder refuses an unknown key and an
// empty block, and the locked ceiling matches nothing and is read-only.
func TestDecodeCallerCeilingAndLockedCeiling(t *testing.T) {
	_, err := DecodeCallerCeiling("")
	assert.Err(t, err)
	_, err = DecodeCallerCeiling("projecs: [a]")
	assert.Err(t, err)
	ceil, err := DecodeCallerCeiling("projects: [a-*]\nread_only: true\n")
	assert.NoErr(t, err)
	assert.True(t, ceil.AllowsProject("a-1"))
	assert.True(t, ceil.IsReadOnly())

	locked := LockedCallerCeiling()
	assert.False(t, locked.AllowsProject("a-1"))
	assert.False(t, locked.AllowsWorker("w1"))
	assert.False(t, locked.AllowsAgent("codex"))
	assert.False(t, locked.AllowsRunner("local"))
	assert.False(t, locked.AllowsRunner("w1"))
	assert.True(t, locked.IsReadOnly())
	assert.False(t, locked.ExecAllowed())
	assert.False(t, locked.GlobalMemoryAllowed())
	_, err = DecodeCallerCeiling(LockedCallerCeilingYAML())
	assert.NoErr(t, err)
}

// TestValidCallerID: the id rule shared with worker ids, plus the reserved words and the
// job-credential prefix.
func TestValidCallerID(t *testing.T) {
	for _, ok := range []string{"ci", "ci-2", "A.b_c", strings.Repeat("x", 64)} {
		assert.NoErr(t, ValidCallerID(ok), ok)
	}
	for _, bad := range []string{"", "-ci", "default", "SYSTEM", "root", "job-abc", "ci/1", strings.Repeat("x", 65)} {
		assert.Err(t, ValidCallerID(bad), bad)
	}
}
