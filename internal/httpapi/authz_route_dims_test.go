package httpapi

import (
	"net/http"
	"sort"
	"strings"
	"testing"
)

// reviewedReadSafe lists every non-GET route that is explicitly ReadSafe. A new override
// must be added here on purpose, so it shows up in review.
var reviewedReadSafe = map[string]string{
	"POST /v1/ws-ticket":                        "mints a ticket for the caller's own live connection; stores nothing about a project",
	"POST /v1/push/subscriptions":               "the caller's own browser push subscription (personal preference)",
	"DELETE /v1/push/subscriptions":             "the caller's own browser push subscription (personal preference)",
	"PUT /v1/workbench/layout":                  "the caller's own workbench layout (personal UI preference)",
	"POST /v1/workbench/threads/seen-all":       "the caller's own seen baseline (personal read mark)",
	"PATCH /v1/workbench/threads/{id}":          "the caller's own title / pin / seen preference of a thread",
	"POST /v1/today/snooze":                     "the caller's own snooze of a card (personal preference)",
	"DELETE /v1/today/snooze/{key}":             "the caller's own snooze of a card (personal preference)",
	"POST /v1/workflow-templates/{name}/render": "renders a template to a spec and returns it; nothing is stored or started",
	"POST /v1/attachments":                      "uploads an unsent image that belongs to the uploader alone until it is sent; touches no project state",
	"DELETE /v1/attachments/{id}":               "the uploader deletes their own unsent image (personal file); a sent one is refused",
	"POST /v1/mail/{id}/ack":                    "the recipient's own read mark of a letter (reading is delivery); changes nothing for anybody else",
}

// reviewedMutatingGET lists every GET route that is explicitly Mutating: data-plane
// connections that can carry writes.
var reviewedMutatingGET = map[string]string{
	"GET /v1/jobs/{id}/attach":       "pty attach: the connection can type into the job's terminal",
	"GET /v1/tunnels/connect":        "tunnel data plane: opens a forwarded connection",
	"GET /v1/workers/connect":        "worker control channel",
	"GET /v1/workers/tunnel-connect": "worker side of the tunnel data plane",
	"GET /v1/workers/pty-connect":    "worker side of the pty data plane",
}

// TestEveryRouteDeclaresScope: the coverage rule of the project-scope dimension — no
// declaration may leave scope unset, and a scope that names an argument must carry it.
func TestEveryRouteDeclaresScope(t *testing.T) {
	var missing, noArg []string
	for key, decl := range routeDecls {
		switch decl.scope.kind {
		case scopeUnset:
			missing = append(missing, key)
		case scopePathProject, scopeResource:
			if decl.scope.arg == "" {
				noArg = append(noArg, key)
			}
		default:
			if decl.scope.arg != "" {
				noArg = append(noArg, key+" (unexpected argument)")
			}
		}
		if decl.scope.kind == scopePathProject {
			_, path, _ := strings.Cut(key, " ")
			if !strings.Contains(path, "{"+decl.scope.arg+"}") {
				noArg = append(noArg, key+" (PathProject names a parameter the path does not have)")
			}
		}
	}
	sort.Strings(missing)
	sort.Strings(noArg)
	if len(missing) > 0 {
		t.Errorf("routes without a project scope (declare scope: PathProject / Resource / BodyProject / Filter / NoProject):\n  %s", strings.Join(missing, "\n  "))
	}
	if len(noArg) > 0 {
		t.Errorf("routes with a malformed project scope:\n  %s", strings.Join(noArg, "\n  "))
	}
}

// TestRouteReadWriteClass: the default is by method (GET / HEAD read, the rest write), an
// override must differ from that default (a redundant one is noise), and every override is
// on the reviewed lists above, in both directions.
func TestRouteReadWriteClass(t *testing.T) {
	for key, decl := range routeDecls {
		method, _, _ := strings.Cut(key, " ")
		want := rwWrite
		if method == http.MethodGet || method == http.MethodHead {
			want = rwRead
		}
		if decl.rw == rwDefault {
			if got := decl.effectiveRW(key); got != want {
				t.Errorf("%s: effective class %v without override, want the method default %v", key, got, want)
			}
			continue
		}
		if decl.rw == want {
			t.Errorf("%s: explicit %v equals the method default; drop the redundant override", key, decl.rw)
		}
		if got := decl.effectiveRW(key); got != decl.rw {
			t.Errorf("%s: effective class %v ignores the override %v", key, got, decl.rw)
		}
		switch decl.rw {
		case rwRead:
			if _, ok := reviewedReadSafe[key]; !ok {
				t.Errorf("%s: ReadSafe override is not on reviewedReadSafe (add it there, with a reason, in the same change)", key)
			}
		case rwWrite:
			if _, ok := reviewedMutatingGET[key]; !ok {
				t.Errorf("%s: Mutating override is not on reviewedMutatingGET (add it there, with a reason, in the same change)", key)
			}
		}
	}
	for key := range reviewedReadSafe {
		if decl, ok := routeDecls[key]; !ok || decl.rw != rwRead {
			t.Errorf("reviewedReadSafe lists %s, which is not declared ReadSafe", key)
		}
	}
	for key := range reviewedMutatingGET {
		if decl, ok := routeDecls[key]; !ok || decl.rw != rwWrite {
			t.Errorf("reviewedMutatingGET lists %s, which is not declared Mutating", key)
		}
	}
}
