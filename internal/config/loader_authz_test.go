package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// Loader rules of the permission design (plan T1.2, design §3「loader 规则」/「类型隔离」).

// TestLoadGateSatisfiedByRootAdmin: an open capability gate is satisfied by a root
// administrator (`can_admin: true`), which holds every permission — the extra per-bit
// holder stays valid too, so an existing deployment keeps loading unchanged.
func TestLoadGateSatisfiedByRootAdmin(t *testing.T) {
	tests := []struct {
		name string
		src  string
	}{
		{
			name: "answer gate + root",
			src: `
server:
  governance:
    require_answer_capability: true
  callers:
    - id: boss
      token: b
      can_admin: true
`,
		},
		{
			name: "admin gate + root",
			src: `
server:
  governance:
    require_admin_capability: true
  callers:
    - id: boss
      token: b
      can_admin: true
`,
		},
		{
			name: "attach gate + root",
			src: `
server:
  governance:
    require_attach_capability: true
  callers:
    - id: boss
      token: b
      can_admin: true
`,
		},
		{
			name: "tunnel gate + root",
			src: `
server:
  governance:
    require_tunnel_capability: true
  callers:
    - id: boss
      token: b
      can_admin: true
`,
		},
		{
			name: "all four gates open, single root, no capability bits",
			src: `
server:
  token: t
  governance:
    require_answer_capability: true
    require_admin_capability: true
    require_attach_capability: true
    require_tunnel_capability: true
  callers:
    - id: boss
      token: b
      can_admin: true
    - id: agent-dev
      token: a
`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "cfg.yaml")
			write(t, p, tc.src)
			if _, _, err := Load(p); err != nil {
				t.Fatalf("Load should accept a root administrator in place of the capability bit: %v", err)
			}
		})
	}
}

// TestLoadGateWithoutBitOrRootStillFails: the relaxed rule does not give up the lockout
// guard — with no bit holder and no root administrator the gate would refuse everybody.
func TestLoadGateWithoutBitOrRootStillFails(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "declared callers, no root",
			src: `
server:
  governance:
    require_answer_capability: true
  callers:
    - id: readonly
      token: ro
`,
			want: "no root administrator",
		},
		{
			name: "anonymous deployment has no root",
			src: `
server:
  allow_empty_token: true
  governance:
    require_tunnel_capability: true
`,
			want: "no root administrator",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "cfg.yaml")
			write(t, p, tc.src)
			_, _, err := Load(p)
			if err == nil {
				t.Fatal("Load should reject a gate nobody can pass")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestLoadRejectsCallerIDMatchingWorkerID: a caller id and a worker id are two
// authenticating identities, and the worker entry is resolved first — sharing one id would
// shadow the caller's own token (design §3「类型隔离」).
func TestLoadRejectsCallerIDMatchingWorkerID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, `
server:
  callers:
    - id: w1
      token: caller-token
  workers:
    w1:
      token: worker-token
`)
	_, _, err := Load(p)
	if err == nil {
		t.Fatal("Load should reject a caller id that is also a worker id")
	}
	if !strings.Contains(err.Error(), `caller id "w1" is also a server.workers id`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestLoadAllowsDistinctCallerAndWorkerIDs: the guard must not reject the ordinary case.
func TestLoadAllowsDistinctCallerAndWorkerIDs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "cfg.yaml")
	write(t, p, `
server:
  callers:
    - id: human
      token: caller-token
  workers:
    w-docker:
      token: worker-token
`)
	if _, _, err := Load(p); err != nil {
		t.Fatalf("Load should accept distinct caller and worker ids: %v", err)
	}
}
