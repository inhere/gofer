package authz

import (
	"slices"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// without returns every name of all that is not in drop, keeping the order of all.
func without(all []string, drop ...string) []string {
	out := make([]string, 0, len(all))
	for _, name := range all {
		if slices.Contains(drop, name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// designAllGatesOff is the design §3 table with every switch closed: the `users` subject
// gets exactly these, so no caller needs a capability bit. Written out literally (not
// recomputed from the group variables) so an accidental group edit fails here.
var designAllGatesOff = []string{
	PermJobAttach,
	PermJobReview,
	PermJobWakeOthers,
	PermInteractionAnswer,
	PermTrackerDelete,
	PermTunnelOpen,
	PermUpdateApply,
	PermWorkerUpgrade,
	PermWorkerUpgradeUpload,
	PermWorkerManage,
	PermConfigWrite,
	PermProjectWrite,
	PermRulesWrite,
	PermSkillsWrite,
}

// TestDeriveGateGroups pins the「配置推出」columns of design §3 row by row: with a gate
// closed the group goes to `users`, with a gate open only the matching capability holder
// gets it (and the admin group goes to nobody but the root administrator).
func TestDeriveGateGroups(t *testing.T) {
	t.Parallel()
	all := []config.CallerConfig{
		{ID: "bob", Token: "tok-bob"},
		{ID: "answ", Token: "tok-answ", CanAnswer: true},
		{ID: "att", Token: "tok-att", CanAttach: true},
		{ID: "tun", Token: "tok-tun", CanTunnel: true},
	}
	tests := []struct {
		name      string
		gates     config.GovernanceConfig
		wantUsers []string
		want      map[string][]string
	}{
		{
			name:      "all gates closed",
			wantUsers: designAllGatesOff,
			want:      map[string][]string{},
		},
		{
			name:      "answer gate open",
			gates:     config.GovernanceConfig{RequireAnswerCapability: true},
			wantUsers: without(designAllGatesOff, answerGroup...),
			// `session.reply.others` follows can_answer too (design §3), and nobody else.
			want: map[string][]string{"answ": {PermJobReview, PermJobWakeOthers, PermInteractionAnswer, PermSessionReplyOthers}},
		},
		{
			name:      "attach gate open",
			gates:     config.GovernanceConfig{RequireAttachCapability: true},
			wantUsers: without(designAllGatesOff, attachGroup...),
			want:      map[string][]string{"att": {PermJobAttach}},
		},
		{
			name:      "tunnel gate open",
			gates:     config.GovernanceConfig{RequireTunnelCapability: true},
			wantUsers: without(designAllGatesOff, tunnelGroup...),
			want:      map[string][]string{"tun": {PermTunnelOpen}},
		},
		{
			name:      "admin gate open",
			gates:     config.GovernanceConfig{RequireAdminCapability: true},
			wantUsers: without(designAllGatesOff, adminGroup...),
			// Only the root administrator keeps the admin group when the gate is open, so
			// there is no per-caller grant to record.
			want: map[string][]string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.ServerConfig{Callers: all, Governance: tc.gates}
			d := Derive(cfg, true)
			if got := d.Users(); !slices.Equal(got, tc.wantUsers) {
				t.Errorf("users grants = %v, want %v", got, tc.wantUsers)
			}
			for _, cc := range all {
				want := tc.want[cc.ID]
				if got := d.For(cc.ID); !slices.Equal(got, want) {
					t.Errorf("caller %s grants = %v, want %v", cc.ID, got, want)
				}
				// A caller's own grants also reach Effective with the gate-independent
				// `users` floor underneath.
				if got, wantEff := d.Effective(cc.ID), catalogOrder(append(slices.Clone(tc.wantUsers), want...)); !slices.Equal(got, wantEff) {
					t.Errorf("caller %s effective = %v, want %v", cc.ID, got, wantEff)
				}
			}
		})
	}
}

// TestDeriveRoots pins the root-administrator rule of design §2/§3 across the three
// deployment shapes: declared can_admin callers, a single-token deployment (implicit
// `default`), and an anonymous allow_empty_token deployment (no root at all).
func TestDeriveRoots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		cfg         *config.ServerConfig
		legacyToken bool
		want        []string
	}{
		{name: "nil config", cfg: nil, want: nil},
		{
			name: "single token deployment: default is root",
			cfg:  &config.ServerConfig{Token: "t"}, legacyToken: true,
			want: []string{DefaultCallerID},
		},
		{
			name: "no token at all: no root",
			cfg:  &config.ServerConfig{},
		},
		{
			name: "anonymous deployment: no root",
			cfg:  &config.ServerConfig{AllowEmptyToken: true},
		},
		{
			name: "declared can_admin caller is root",
			cfg: &config.ServerConfig{Callers: []config.CallerConfig{
				{ID: "human", Token: "tok-human"},
				{ID: "boss", Token: "tok-boss", CanAdmin: true},
			}},
			legacyToken: true,
			want:        []string{"boss"},
		},
		{
			name: "callers without can_admin: no root",
			cfg: &config.ServerConfig{Callers: []config.CallerConfig{
				{ID: "agent-dev", Token: "tok-agent", CanAnswer: true},
			}},
			legacyToken: true,
		},
		{
			name: "legacy token plus declared callers: default is not root",
			cfg: &config.ServerConfig{Callers: []config.CallerConfig{
				{ID: "agent-dev", Token: "tok-agent"},
			}},
			legacyToken: true,
		},
		{
			name: "declared can_admin keeps root even with allow_empty_token",
			cfg: &config.ServerConfig{AllowEmptyToken: true, Callers: []config.CallerConfig{
				{ID: "boss", Token: "tok-boss", CanAdmin: true},
			}},
			want: []string{"boss"},
		},
		{
			name: "two roots, sorted",
			cfg: &config.ServerConfig{Callers: []config.CallerConfig{
				{ID: "zed", Token: "tok-z", CanAdmin: true},
				{ID: "amy", Token: "tok-a", CanAdmin: true},
				// A tokenless declaration names the legacy token's id: it is a root too.
				{ID: "default", CanAdmin: true},
			}},
			legacyToken: true,
			want:        []string{"amy", "default", "zed"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := Derive(tc.cfg, tc.legacyToken)
			if got := d.Roots(); !slices.Equal(got, tc.want) {
				t.Errorf("roots = %v, want %v", got, tc.want)
			}
			for _, id := range []string{"", "default", "unknown"} {
				wantRoot := slices.Contains(tc.want, id) && id != ""
				if got := d.IsRoot(id); got != wantRoot {
					t.Errorf("IsRoot(%q) = %v, want %v", id, got, wantRoot)
				}
			}
		})
	}
}

// TestDeriveRootHasEveryPermission: the root administrator holds the whole catalog,
// auth.manage included, whatever the switches say (design decision 4).
func TestDeriveRootHasEveryPermission(t *testing.T) {
	t.Parallel()
	cfg := &config.ServerConfig{
		Callers: []config.CallerConfig{{ID: "boss", Token: "tok", CanAdmin: true}, {ID: "bob", Token: "tok2"}},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireAttachCapability: true, RequireTunnelCapability: true,
		},
	}
	d := Derive(cfg, true)
	want := make([]string, 0, len(catalog))
	for _, p := range catalog {
		want = append(want, p.Name)
	}
	if got := d.Effective("boss"); !slices.Equal(got, want) {
		t.Errorf("root effective = %v, want the whole catalog %v", got, want)
	}
	// Every gate is open and bob holds no bit, so he only has what the database gives
	// him — nothing here.
	if got := d.Effective("bob"); len(got) != 0 {
		t.Errorf("bob effective = %v, want none with every gate open", got)
	}
	// Grant management is never derived: nobody but a root administrator has it.
	if d.Has("bob", PermAuthManage) {
		t.Error("auth.manage must never be derived for a non-root caller")
	}
}

// TestDeriveDeprecations: the capability bits and the four governance switches of the old
// style are reported while in use (G032), and can_admin is not one of them.
func TestDeriveDeprecations(t *testing.T) {
	t.Parallel()
	cfg := &config.ServerConfig{
		Callers: []config.CallerConfig{
			{ID: "bob", Token: "tok", CanAnswer: true, CanAdmin: true, CanAttach: true},
		},
		Governance: config.GovernanceConfig{
			RequireAnswerCapability: true, RequireAdminCapability: true,
			RequireTunnelCapability: true,
		},
	}
	got := Derive(cfg, true).Deprecations()
	want := []Deprecated{
		{Key: "server.callers[].can_answer", Caller: "bob"},
		{Key: "server.callers[].can_attach", Caller: "bob"},
		{Key: "server.governance.require_answer_capability"},
		{Key: "server.governance.require_admin_capability"},
		{Key: "server.governance.require_tunnel_capability"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("deprecations = %v, want %v (can_admin must not be reported: it names the root)", got, want)
	}
	if got := Derive(&config.ServerConfig{Callers: []config.CallerConfig{{ID: "boss", CanAdmin: true}}}, true).Deprecations(); len(got) != 0 {
		t.Errorf("a can_admin-only config reported deprecations: %v", got)
	}
}

// TestCatalogGroupsCoverEveryConfigPermission is the invariant behind the derivation: the
// `config`-source permissions of the catalog are exactly the gate groups, and the
// `root`-source ones are exactly the strict group — so a new catalog row must be placed
// in design §3 before it can be derived.
func TestCatalogGroupsCoverEveryConfigPermission(t *testing.T) {
	t.Parallel()
	grouped := make(map[string]int)
	for _, group := range [][]string{answerGroup, replyGroup, attachGroup, tunnelGroup, adminGroup} {
		for _, name := range group {
			grouped[name]++
		}
	}
	for _, p := range catalog {
		switch p.Source {
		case SourceConfig:
			if grouped[p.Name] != 1 {
				t.Errorf("config-source permission %s is in %d gate groups, want exactly 1", p.Name, grouped[p.Name])
			}
		case SourceRoot:
			if p.Name == PermAuthManage {
				continue
			}
			if !slices.Contains(rootOnlyGroup(), p.Name) {
				t.Errorf("root-source permission %s is missing from the strict group", p.Name)
			}
		}
	}
	for name := range grouped {
		p, ok := Lookup(name)
		if !ok {
			t.Errorf("gate group names unknown permission %s", name)
			continue
		}
		if p.Source != SourceConfig {
			t.Errorf("gate group holds %s, whose catalog source is %s, want config", name, p.Source)
		}
	}
}
