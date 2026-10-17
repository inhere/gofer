package httpapi

import (
	"net/http"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

// A caller display name is display only: whoami and the grant list carry it, the id stays.
func TestCallerDisplayNamesInAuthViews(t *testing.T) {
	t.Parallel()
	s := newTestServerCfg(t, config.ServerConfig{Callers: []config.CallerConfig{
		{ID: "root", Name: "admin", Token: "tok-root", CanAdmin: true},
		{ID: "alice", Token: "tok-alice"},
	}})

	var who authWhoamiView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-root", nil), &who)
	if who.CallerID != "root" || who.CallerName != "admin" {
		t.Fatalf("whoami = id %q name %q, want root/admin", who.CallerID, who.CallerName)
	}
	if who.CallerNames["root"] != "admin" || len(who.CallerNames) != 1 {
		t.Errorf("caller_names = %v, want only root->admin", who.CallerNames)
	}

	who = authWhoamiView{}
	decode(t, do(t, s, http.MethodGet, "/v1/auth/whoami", "tok-alice", nil), &who)
	if who.CallerID != "alice" || who.CallerName != "" {
		t.Errorf("an unnamed caller must report no name, got %q", who.CallerName)
	}

	var list authGrantsView
	decode(t, do(t, s, http.MethodGet, "/v1/auth/grants", "tok-root", nil), &list)
	if list.CallerNames["root"] != "admin" {
		t.Errorf("grants caller_names = %v, want root->admin", list.CallerNames)
	}
}
