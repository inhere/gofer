package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 调用方上限（ceiling）的配置侧用例：查找与判定纯函数、严格解码、给 root 写上限报错、
// glob 校验、显式允许本机 runner 的警告、Clone 深拷贝、保存往返、字段策略（仅文件 + 热加载）。

// ceilingFixture is the smallest config that declares two callers, one of them with a
// ceiling: `ci` is the restricted one, `plain` has none (the regression baseline).
const ceilingFixture = `projects:
  demo:
    host_path: /tmp/demo
server:
  token: tok-main
  callers:
    - id: plain
      token: tok-plain
    - id: ci
      token: tok-ci
      ceiling:
        projects: ["app-*", "docs"]
        runners: ["w-linux-*"]
        workers: ["w-linux-*"]
        agents: ["codex", "claude*"]
        allow_exec: true
        global_memory: true
        read_only: true
`

func loadCeilingConfig(t *testing.T, body string) (*Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, body)
	cfg, _, err := Load(p)
	return cfg, err
}

// TestCallerCeilingLookup pins the lookup: only a declared caller WITH a ceiling answers
// ok=true, and CeilingCallers lists exactly those ids (the serve startup log reads it).
func TestCallerCeilingLookup(t *testing.T) {
	cfg, err := loadCeilingConfig(t, ceilingFixture)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ceil, ok := cfg.Server.CallerCeiling("ci")
	if !ok || ceil == nil {
		t.Fatalf("CallerCeiling(ci) = %v ok=%v, want the configured ceiling", ceil, ok)
	}
	if !slices.Equal(ceil.Projects, []string{"app-*", "docs"}) || !slices.Equal(ceil.Agents, []string{"codex", "claude*"}) {
		t.Fatalf("ceiling = %+v, want the authored lists", ceil)
	}
	if _, ok := cfg.Server.CallerCeiling("plain"); ok {
		t.Error("a caller without a ceiling block must answer ok=false")
	}
	if _, ok := cfg.Server.CallerCeiling("nobody"); ok {
		t.Error("an undeclared caller must answer ok=false")
	}
	if _, ok := cfg.Server.CallerCeiling(""); ok {
		t.Error("the empty caller id must answer ok=false")
	}
	if got := cfg.Server.CeilingCallers(); !slices.Equal(got, []string{"ci"}) {
		t.Errorf("CeilingCallers() = %v, want [ci]", got)
	}
	var nilServer *ServerConfig
	if _, ok := nilServer.CallerCeiling("ci"); ok || nilServer.CeilingCallers() != nil {
		t.Error("a nil ServerConfig has no ceilings")
	}
}

// TestCallerCeilingPredicates is the semantics table: every dimension's empty list, its
// glob matching, the machine-runner rule (both spellings) and the three strict defaults.
func TestCallerCeilingPredicates(t *testing.T) {
	t.Run("no ceiling restricts nothing", func(t *testing.T) {
		var none *CallerCeiling
		for _, got := range []bool{
			none.AllowsProject("anything"), none.AllowsRunner(BuiltinLocalRunner), none.AllowsRunner("w-1"),
			none.AllowsWorker("w-1"), none.AllowsAgent("codex"),
			none.ExecAllowed(), none.GlobalMemoryAllowed(),
		} {
			if !got {
				t.Fatal("a nil ceiling (no ceiling configured) must not restrict anything")
			}
		}
		if none.IsReadOnly() {
			t.Fatal("a nil ceiling is not a read-only switch")
		}
	})

	t.Run("empty lists", func(t *testing.T) {
		// projects / workers / agents empty = that dimension is not restricted.
		empty := &CallerCeiling{}
		if !empty.AllowsProject("anything") || !empty.AllowsWorker("w-1") || !empty.AllowsAgent("codex") {
			t.Error("an empty list must not restrict projects / workers / agents")
		}
		// runners empty = every runner EXCEPT the machine runner.
		if empty.AllowsRunner(BuiltinLocalRunner) || empty.AllowsRunner(BuiltinLocalRunnerAlias) {
			t.Error("an empty runners list must NOT allow the machine runner")
		}
		if !empty.AllowsRunner("w-linux-1") {
			t.Error("an empty runners list allows worker runners")
		}
		// The three switches default to the strict side when the ceiling is present.
		if empty.ExecAllowed() || empty.GlobalMemoryAllowed() || empty.IsReadOnly() {
			t.Error("an omitted allow_exec / global_memory / read_only must resolve strictly")
		}
	})

	t.Run("globs and explicit machine runner", func(t *testing.T) {
		c := &CallerCeiling{
			Projects:     []string{"app-*", "docs"},
			Runners:      []string{"w-linux-*", BuiltinLocalRunnerAlias}, // the CLI spelling must work
			Workers:      []string{"w-linux-*"},
			Agents:       []string{"claude*"},
			AllowExec:    boolPtr(true),
			GlobalMemory: boolPtr(true),
			ReadOnly:     boolPtr(true),
		}
		for _, tc := range []struct {
			name string
			got  bool
			want bool
		}{
			{"project in glob", c.AllowsProject("app-web"), true},
			{"project literal", c.AllowsProject("docs"), true},
			{"project outside", c.AllowsProject("infra"), false},
			{"project empty", c.AllowsProject(""), false},
			{"runner worker glob", c.AllowsRunner("w-linux-2"), true},
			{"runner local canonical", c.AllowsRunner(BuiltinLocalRunner), true},
			{"runner local alias", c.AllowsRunner(BuiltinLocalRunnerAlias), true},
			{"runner outside", c.AllowsRunner("w-mac-1"), false},
			{"worker glob", c.AllowsWorker("w-linux-2"), true},
			{"worker empty", c.AllowsWorker(""), false},
			{"agent glob", c.AllowsAgent("claude-acp"), true},
			{"agent outside", c.AllowsAgent("codex"), false},
			{"exec on", c.ExecAllowed(), true},
			{"global memory on", c.GlobalMemoryAllowed(), true},
			{"read only on", c.IsReadOnly(), true},
		} {
			if tc.got != tc.want {
				t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
			}
		}
	})

	t.Run("runners list without local forbids the machine runner", func(t *testing.T) {
		c := &CallerCeiling{Runners: []string{"w-linux-*"}}
		if c.AllowsRunner(BuiltinLocalRunner) || c.AllowsRunner(BuiltinLocalRunnerAlias) {
			t.Error("a runners list that does not name the built-in runner must refuse it")
		}
	})

	t.Run("wildcard matches the empty value only when it says so", func(t *testing.T) {
		any := &CallerCeiling{Workers: []string{"*"}}
		if !any.AllowsWorker("") || !any.AllowsWorker("w-1") {
			t.Error("`*` matches any value, the empty one included")
		}
	})

	t.Run("malformed patterns match nothing", func(t *testing.T) {
		bad := &CallerCeiling{Projects: []string{"[a"}}
		if bad.AllowsProject("[a") || bad.AllowsProject("anything") {
			t.Error("an unparsable pattern must never match (the load refuses it)")
		}
	})
}

// TestCallerCeilingStrictDecode: the ceiling sub-tree is the one place a typo'd key fails
// the load, and an empty ceiling is refused; every other unknown key keeps the loose
// behaviour the loader documents.
func TestCallerCeilingStrictDecode(t *testing.T) {
	t.Run("unknown key fails and names it", func(t *testing.T) {
		_, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: ci
      token: tok-ci
      ceiling:
        projects: ["app"]
        projecs: ["typo"]
`)
		if err == nil {
			t.Fatal("an unknown ceiling key must fail the load")
		}
		for _, want := range []string{`server.callers["ci"].ceiling`, "projecs"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to name %q", err, want)
			}
		}
	})

	t.Run("unknown key inside an include fragment names the file", func(t *testing.T) {
		dir := t.TempDir()
		writeFiles(t, dir, map[string]string{
			"config.yaml":       "include:\n  - config.d/*.yaml\n",
			"config.d/srv.yaml": "server:\n  token: tok-main\n  callers:\n    - id: ci\n      token: tok-ci\n      ceiling:\n        runners: [local]\n        nope: 1\n",
		})
		_, _, err := Load(filepath.Join(dir, "config.yaml"))
		if err == nil || !strings.Contains(err.Error(), "srv.yaml") || !strings.Contains(err.Error(), "nope") {
			t.Fatalf("err = %v, want the fragment file and the unknown key", err)
		}
	})

	t.Run("empty ceiling is refused", func(t *testing.T) {
		for name, body := range map[string]string{
			"flow mapping": "      ceiling: {}\n",
			"bare key":     "      ceiling:\n",
		} {
			_, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: ci
      token: tok-ci
`+body)
			if err == nil || !strings.Contains(err.Error(), "empty ceiling") {
				t.Errorf("%s: err = %v, want an empty-ceiling refusal", name, err)
			}
		}
	})

	t.Run("other unknown keys stay tolerated", func(t *testing.T) {
		// The rest of the document keeps the non-strict decode (an operator's stray key must
		// not brick a running server) — including a stray key next to the ceiling itself.
		cfg, err := loadCeilingConfig(t, `server:
  token: tok-main
  nope: 1
  callers:
    - id: ci
      token: tok-ci
      typo: 2
      ceiling:
        runners: ["w-*"]
`)
		if err != nil {
			t.Fatalf("a stray key outside the ceiling must not fail the load: %v", err)
		}
		if _, ok := cfg.Server.CallerCeiling("ci"); !ok {
			t.Fatal("the ceiling must still be read")
		}
	})
}

// TestCallerCeilingValidation: glob legality and the root rule.
func TestCallerCeilingValidation(t *testing.T) {
	t.Run("root administrator cannot carry a ceiling", func(t *testing.T) {
		_, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: boss
      token: tok-boss
      can_admin: true
      ceiling:
        read_only: true
`)
		if err == nil {
			t.Fatal("a ceiling on the root administrator must fail the load")
		}
		for _, want := range []string{"boss", "root administrator", "ceiling"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %v, want it to mention %q", err, want)
			}
		}
		// The same caller without can_admin loads: it is not the root, so a ceiling applies.
		if _, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: boss
      token: tok-boss
      ceiling:
        read_only: true
`); err != nil {
			t.Fatalf("a non-root caller must be able to carry a ceiling: %v", err)
		}
	})

	t.Run("invalid glob pattern", func(t *testing.T) {
		for _, dim := range []string{"projects", "runners", "workers", "agents"} {
			_, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: ci
      token: tok-ci
      ceiling:
        `+dim+`: ["[bad"]
`)
			if err == nil || !strings.Contains(err.Error(), "ceiling."+dim) {
				t.Errorf("%s: err = %v, want an invalid-pattern refusal naming the dimension", dim, err)
			}
		}
	})

	t.Run("empty pattern", func(t *testing.T) {
		_, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: ci
      token: tok-ci
      ceiling:
        agents: ["codex", ""]
`)
		if err == nil || !strings.Contains(err.Error(), "empty pattern") {
			t.Fatalf("err = %v, want an empty-pattern refusal", err)
		}
	})
}

// TestCallerCeilingLocalRunnerWarning: explicitly allowing the machine runner is a warning
// on the load-warning channel (a ceiling there is not a security boundary), and nothing
// else warns.
func TestCallerCeilingLocalRunnerWarning(t *testing.T) {
	cfg, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: ci
      token: tok-ci
      ceiling:
        runners: [server, "w-*"]
`)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	ws := cfg.LoadWarnings()
	if len(ws) != 1 {
		t.Fatalf("warnings = %q, want one for the machine runner", ws)
	}
	for _, want := range []string{"ci", BuiltinLocalRunner, "not a security boundary"} {
		if !strings.Contains(ws[0], want) {
			t.Errorf("warning %q must mention %q", ws[0], want)
		}
	}
	if got := cfg.Clone().LoadWarnings(); len(got) != 1 {
		t.Errorf("Clone dropped the ceiling warning: %q", got)
	}

	// A worker-only runners list (and a ceiling with no runners key at all) warns nothing.
	for name, body := range map[string]string{
		"worker only": "      runners: [\"w-*\"]\n",
		"no runners":  "      projects: [\"app-*\"]\n",
		"no ceiling":  "",
	} {
		cfg, err := loadCeilingConfig(t, `server:
  token: tok-main
  callers:
    - id: ci
      token: tok-ci
`+body)
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if ws := cfg.LoadWarnings(); len(ws) != 0 {
			t.Errorf("%s: warnings = %q, want none", name, ws)
		}
	}
}

// TestCallerCeilingCloneDeepCopies: a clone must not share the ceiling's lists or switches
// with the generation it was cloned from (the cloneServer contract).
func TestCallerCeilingCloneDeepCopies(t *testing.T) {
	cfg, err := loadCeilingConfig(t, ceilingFixture)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	clone := cfg.Clone()
	ceil, ok := clone.Server.CallerCeiling("ci")
	if !ok {
		t.Fatal("the clone lost the ceiling")
	}
	ceil.Projects[0] = "hacked"
	*ceil.ReadOnly = false
	ceil.Runners = append(ceil.Runners, BuiltinLocalRunner)
	live, _ := cfg.Server.CallerCeiling("ci")
	if live.Projects[0] != "app-*" {
		t.Errorf("the live ceiling shares its projects list with the clone: %v", live.Projects)
	}
	if !live.IsReadOnly() {
		t.Error("the live ceiling shares its read_only switch with the clone")
	}
	if slices.Contains(live.Runners, BuiltinLocalRunner) {
		t.Errorf("the live ceiling shares its runners list with the clone: %v", live.Runners)
	}
	// A caller without a ceiling stays without one after a clone.
	if c, ok := clone.Server.CallerCeiling("plain"); ok || c != nil {
		t.Errorf("clone of a caller without a ceiling = %v ok=%v", c, ok)
	}
}

// TestCallerCeilingSurvivesSave: the console save path re-renders the server block from
// the struct, so a ceiling must round-trip through Save unchanged (a save that dropped it
// would silently lift a limit).
func TestCallerCeilingSurvivesSave(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, ceilingFixture)
	cfg, _, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Save(p, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded, _, err := Load(p)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ceil, ok := reloaded.Server.CallerCeiling("ci")
	if !ok {
		t.Fatalf("the saved file lost the ceiling:\n%s", read(t, p))
	}
	if !slices.Equal(ceil.Projects, []string{"app-*", "docs"}) ||
		!slices.Equal(ceil.Runners, []string{"w-linux-*"}) ||
		!slices.Equal(ceil.Agents, []string{"codex", "claude*"}) ||
		!ceil.ExecAllowed() || !ceil.GlobalMemoryAllowed() || !ceil.IsReadOnly() {
		t.Fatalf("ceiling after the save round-trip = %+v", ceil)
	}
}

// TestCallerCeilingFieldPolicy: the ceiling is file-only (the console write endpoint must
// refuse it) and hot-reloadable, and the lookup resolves a caller id as the wildcard
// segment.
func TestCallerCeilingFieldPolicy(t *testing.T) {
	for _, path := range []string{"server.callers.*.ceiling", "server.callers.ci.ceiling", "server.callers.any-key.ceiling"} {
		fp, ok := FieldPolicyFor(path)
		if !ok {
			t.Fatalf("FieldPolicyFor(%q) missed", path)
		}
		if fp.Editable {
			t.Errorf("%s must not be writable through the console", path)
		}
		if !fp.HotReload {
			t.Errorf("%s must be classified hot-reloadable", path)
		}
	}
	// An unclassified caller field stays a miss (never a silent "not editable").
	if _, ok := FieldPolicyFor("server.callers.ci.token"); ok {
		t.Error("an unclassified caller field must be a policy miss")
	}
}
