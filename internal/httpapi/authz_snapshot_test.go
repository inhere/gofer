package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/inhere/gofer/internal/authz"
	"github.com/inhere/gofer/internal/config"
)

// The configuration snapshot (plan T1.2, design §3「一致性」): {cfg, callers, root set,
// derived grants} swapped by ONE atomic store, so no request reads half of a reload.

// TestServerConfigSnapshotSwap: a reload replaces the whole projection at once — the
// caller set, the server policy, the root administrators and the derived grants.
func TestServerConfigSnapshotSwap(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: "legacy"})

	// Startup: the single-token deployment, so `default` is the root (design §2).
	if got := s.authzDerivation().Roots(); len(got) != 1 || got[0] != authz.DefaultCallerID {
		t.Fatalf("initial roots = %v, want [%s]", got, authz.DefaultCallerID)
	}
	if !s.rootAdmin(authz.DefaultCallerID) || s.rootAdmin("alice") {
		t.Fatal("root administrator resolved wrongly at startup")
	}

	// Reload: a declared caller becomes the root, `default` stops being one.
	next := &config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "alice", Token: "tok-alice", CanAdmin: true},
		{ID: "agent-dev", Token: "tok-agent"},
	}}
	s.SetServerConfig(next)

	if got := s.serverCfg(); got != next {
		t.Fatalf("serverCfg() = %v, want the reloaded config %p", got, next)
	}
	if got := s.authzDerivation().Roots(); len(got) != 1 || got[0] != "alice" {
		t.Fatalf("roots after reload = %v, want [alice]", got)
	}
	if s.rootAdmin(authz.DefaultCallerID) || !s.rootAdmin("alice") {
		t.Fatal("root administrator not replaced by the reload")
	}
	if _, ok := s.lookupCaller("tok-alice"); !ok {
		t.Error("the reloaded caller token does not authenticate")
	}
	if _, ok := s.lookupCaller("legacy"); !ok {
		t.Error("the legacy server token must keep authenticating")
	}
	// The derivation of the reloaded config is visible through the same snapshot: every
	// gate is closed, so the users grants reach each caller.
	if !s.authzDerivation().Has("agent-dev", authz.PermConfigWrite) {
		t.Error("gate closed: the users grants must reach every caller")
	}

	// A nil config is ignored: the snapshot stays as it was.
	s.SetServerConfig(nil)
	if got := s.serverCfg(); got != next {
		t.Error("SetServerConfig(nil) must not drop the snapshot")
	}
}

// TestServerConfigSnapshotHints: the two operator hints of design §3 (a deployment with no
// root administrator, and a configuration still using deprecated capability keys) come out
// of the snapshot builder.
func TestServerConfigSnapshotHints(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         *config.ServerConfig
		legacyToken bool
		want        []string
		wantAbsent  []string
	}{
		{
			name:        "deprecated bits and switches",
			legacyToken: true,
			cfg: &config.ServerConfig{
				Callers: []config.CallerConfig{{ID: "bob", Token: "tok-bob", CanAnswer: true}},
				Governance: config.GovernanceConfig{
					RequireAnswerCapability: true, RequireTunnelCapability: true,
				},
			},
			want: []string{
				"key=server.callers[].can_answer",
				"key=server.governance.require_answer_capability",
				"key=server.governance.require_tunnel_capability",
				"remove_in=" + authz.DeprecatedRemoveIn,
			},
		},
		{
			name:       "no root administrator",
			cfg:        &config.ServerConfig{Callers: []config.CallerConfig{{ID: "agent-dev", Token: "tok-agent"}}},
			want:       []string{"no root administrator"},
			wantAbsent: []string{"deprecated"},
		},
		{
			name:        "root administrator, nothing deprecated",
			legacyToken: true,
			cfg:         &config.ServerConfig{Token: "t"},
			wantAbsent:  []string{"no root administrator", "deprecated"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logged := &captureLog{}
			logSnapshotHints(slog.New(logged), authz.Derive(tc.cfg, tc.legacyToken))
			for _, want := range tc.want {
				if !strings.Contains(logged.text(), want) {
					t.Errorf("hint output %q misses %q", logged.text(), want)
				}
			}
			for _, absent := range tc.wantAbsent {
				if strings.Contains(logged.text(), absent) {
					t.Errorf("hint output %q must not contain %q", logged.text(), absent)
				}
			}
		})
	}
}

// captureLog is a slog.Handler that renders the records it receives, so a test can assert
// on the hints without installing a global logger.
type captureLog struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLog) Enabled(context.Context, slog.Level) bool { return true }

func (c *captureLog) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.Key + "=" + a.Value.String())
		return true
	})
	c.mu.Lock()
	c.lines = append(c.lines, b.String())
	c.mu.Unlock()
	return nil
}

func (c *captureLog) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureLog) WithGroup(string) slog.Handler      { return c }

func (c *captureLog) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// TestServerConfigReloadRace reloads the configuration while other goroutines read the
// snapshot and serve requests. Run with -race: the swap is a single atomic store, so a
// reader must never see the caller set of one generation with the grants of another.
func TestServerConfigReloadRace(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Token: "legacy"})

	// Three generations. Each pairs a caller id with a root set, so a torn read (the
	// caller set of one generation and the derivation of another) shows up as a mismatch.
	gens := []*config.ServerConfig{
		{Callers: []config.CallerConfig{{ID: "alice", Token: "tok-a", CanAdmin: true}}},
		{Callers: []config.CallerConfig{{ID: "bob", Token: "tok-b", CanAdmin: true}}},
		{Callers: []config.CallerConfig{{ID: "carol", Token: "tok-c", CanAdmin: true}}},
		{Callers: []config.CallerConfig{{ID: "dave", Token: "tok-d", CanAdmin: true}}},
	}

	const rounds = 150
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				s.SetServerConfig(gens[(i+r)%len(gens)])
			}
		}(i)
	}
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				snap := s.snap.Load()
				if snap == nil {
					t.Error("no snapshot after a reload")
					return
				}
				roots := snap.authz.Roots()
				if len(roots) != 1 {
					t.Errorf("roots = %v, want exactly one", roots)
					return
				}
				root := roots[0]
				// The declared callers of that same snapshot (the legacy token caller
				// `default` is present in every generation) must name the same caller.
				var declared []string
				for _, ce := range snap.callers {
					if ce.kind == callerKindUser && ce.id != authz.DefaultCallerID {
						declared = append(declared, ce.id)
					}
				}
				if len(declared) != 1 || declared[0] != root {
					// The startup snapshot is a generation as well: the single-token
					// deployment declares no caller and `default` is the root.
					if len(declared) != 0 || root != authz.DefaultCallerID {
						t.Errorf("torn snapshot: declared callers %v with root %v", declared, root)
						return
					}
				}
				// The accessors every handler uses must each answer from a complete
				// snapshot: exactly one root, and it is one of the known generations (`default`
				// is the startup one). They are separate reads on purpose — a request takes
				// ONE snapshot — so they are checked one by one, never against each other.
				d := s.authzDerivation()
				if d == nil || len(d.Roots()) != 1 {
					t.Errorf("authzDerivation() roots = %v, want exactly one", d.Roots())
					return
				}
				if r := d.Roots()[0]; r != authz.DefaultCallerID && r != "alice" && r != "bob" && r != "carol" && r != "dave" {
					t.Errorf("authzDerivation() root = %q, not a generation of this test", r)
					return
				}
				if cfg := s.serverCfg(); cfg == nil || len(cfg.Callers) > 1 {
					t.Errorf("serverCfg() = %+v, want one generation's config", cfg)
					return
				}
				if entries := s.callerEntries(); len(entries) == 0 {
					t.Error("callerEntries() is empty")
					return
				}
				if ids := s.UserCallerIDs(); len(ids) == 0 {
					t.Error("UserCallerIDs() is empty")
					return
				}
				if _, ok := lookupCallerIn(s.callerEntries(), "legacy"); !ok {
					t.Error("the legacy token must keep authenticating through every generation")
					return
				}
			}
		}()
	}
	// One reader keeps serving requests through the router while the config is reloading:
	// the meta handler reads the live policy on every request.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for r := 0; r < rounds; r++ {
			req := httptest.NewRequest(http.MethodGet, "/v1/meta", nil)
			req.Header.Set("Authorization", "Bearer legacy")
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("GET /v1/meta status = %d during reloads, want 200", rec.Code)
				return
			}
		}
	}()
	wg.Wait()
}
