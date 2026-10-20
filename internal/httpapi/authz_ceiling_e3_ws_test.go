package httpapi

import (
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// 调用方上限第二期 E3-1：/v1/ws 连接在建连时固定上限指纹（gofer-7798）。
//
// 连接的帧与主题按建连那一刻的上限判定，所以复核时指纹一变（收窄 / 放宽 / 变只读）就断开，
// 客户端重连并按新范围重新订阅；上限没变的连接保持不动。

// TestWSPinnedToConnectTimeCeiling is E3-1: the push channel records the ceiling it was opened
// under, and a re-judgment that finds another one — narrowed, widened or gone — ends it, so the
// client reconnects and re-subscribes under the range in force. An unchanged ceiling keeps it.
func TestWSPinnedToConnectTimeCeiling(t *testing.T) {
	t.Parallel()
	reloaded := func(limCeil, plainCeil *config.CallerCeiling) *config.ServerConfig {
		return &config.ServerConfig{
			Callers: []config.CallerConfig{
				{ID: "lim", Token: ceilTokLim, Ceiling: limCeil},
				{ID: "plain", Token: ceilTokPlain, Ceiling: plainCeil},
			},
			Governance: config.GovernanceConfig{
				RequireAnswerCapability: true, RequireAdminCapability: true,
				RequireAttachCapability: true, RequireTunnelCapability: true,
			},
		}
	}

	cases := []struct {
		name    string
		owner   string
		reload  *config.CallerCeiling
		dropped bool
	}{
		{"an unchanged ceiling keeps the connection", "lim", &config.CallerCeiling{Projects: []string{"p1"}, Workers: []string{"w-ok"}}, false},
		{"a narrowed ceiling ends it", "lim", &config.CallerCeiling{Projects: []string{"p2"}, Workers: []string{"w-ok"}}, true},
		{"a widened ceiling ends it", "lim", &config.CallerCeiling{Projects: []string{"p1", "p2"}}, true},
		{"becoming read-only ends it", "lim", &config.CallerCeiling{Projects: []string{"p1"}, ReadOnly: boolptr(true)}, true},
		{"a plain connection keeps working while its caller stays unrestricted", "plain", nil, false},
		{"a plain connection ends when a reload gives its caller a ceiling", "plain", &config.CallerCeiling{Projects: []string{"p1"}}, true},
	}
	for _, tc := range cases {
		s := newCeilingServer(t)
		spec := ticketLiveConnSpec(AttachTicketBinding{Caller: tc.owner, CeilingOwner: tc.owner}, liveConnWS, "push")
		spec.ceilingFP, spec.ceilingPinned = ceilingFingerprint(s.wsCeiling(tc.owner)), true
		spec.close = func(string) {}
		h := s.registerLiveConn(spec)
		defer h.release()

		var limCeil, plainCeil *config.CallerCeiling
		if tc.owner == "lim" {
			limCeil = tc.reload
		} else {
			plainCeil = tc.reload
		}
		// SetServerConfig re-judges every long connection synchronously, so the outcome is
		// observable as soon as it returns (no sleep, no polling).
		s.SetServerConfig(reloaded(limCeil, plainCeil))

		if alive := liveConnRegistered(s, h.key); alive == tc.dropped {
			if tc.dropped {
				t.Fatalf("the connection survived a reload that changed its ceiling")
			}
			t.Fatalf("the connection was ended by a reload it should survive")
		}
	}
}

// liveConnRegistered reports whether one registration is still in the table.
func liveConnRegistered(s *Server, key string) bool {
	s.liveConns.mu.Lock()
	defer s.liveConns.mu.Unlock()
	_, ok := s.liveConns.byKey[key]
	return ok
}

// TestCeilingStatsScopeKeyIsStable guards the snapshot / overview cache discrimination: two
// ceilings that admit the same projects fingerprint alike, a different one does not, and read-only
// is part of the fingerprint a pinned connection compares.
func TestCeilingStatsScopeKeyIsStable(t *testing.T) {
	t.Parallel()
	a := ceilingFingerprint(&config.CallerCeiling{Projects: []string{"p1", "p2"}})
	b := ceilingFingerprint(&config.CallerCeiling{Projects: []string{"p2", "p1"}})
	if a != b || a == "" {
		t.Fatalf("same projects must fingerprint alike: %q vs %q", a, b)
	}
	if ro := ceilingFingerprint(&config.CallerCeiling{Projects: []string{"p1", "p2"}, ReadOnly: boolptr(true)}); ro == a {
		t.Fatalf("read-only must change the fingerprint: %q", ro)
	}
	if got := ceilingFingerprint(nil); got != "" {
		t.Fatalf("an unrestricted caller must fingerprint as empty, got %q", got)
	}
	if ceilingScopeKey(sortedCeilingProjects(&config.CallerCeiling{Projects: []string{"p1", "p2"}})) ==
		ceilingScopeKey(sortedCeilingProjects(&config.CallerCeiling{Projects: []string{"p1"}})) {
		t.Fatalf("different project sets must not share a snapshot key")
	}
}
