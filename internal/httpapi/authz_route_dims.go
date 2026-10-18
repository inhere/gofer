// Route declaration dimensions added by caller-scope ceilings phase 1 (design
// docs/design/2026-10-18-caller-scope-ceilings-design.md, plan task S4a): for every route,
// WHERE its project comes from (routeScope) and whether it only reads (rwClass).
//
// S4a is annotation only. Nothing in the gate, the middleware or a handler reads these two
// fields yet; S4b makes the decision logic (canFor, the read-only refusal, list filtering)
// consume them. They are written down first so the coverage test can force every route —
// and every route added later — to state both before any enforcement relies on them.

package httpapi

import (
	"net/http"
	"strings"
)

// scopeKind is how a route's project is determined.
type scopeKind uint8

const (
	// scopeUnset is the zero value: the route has not declared a project scope. The coverage
	// test refuses it.
	scopeUnset scopeKind = iota
	// scopePathProject: the project key is a path parameter (PathProject).
	scopePathProject
	// scopeResource: the project comes from the record the route addresses (Resource).
	scopeResource
	// scopeBodyProject: the project is in the request body; the handler must check it after
	// binding (BodyProject).
	scopeBodyProject
	// scopeFilter: a list / aggregate whose results are narrowed to the caller's scope
	// (Filter).
	scopeFilter
	// scopeNoProject: the route has no project dimension (NoProject) — a server-global
	// admin / meta / self route, or a self-authenticating external one.
	scopeNoProject
)

// routeScope is the project-scope declaration of one route.
type routeScope struct {
	kind scopeKind
	// arg is the path parameter name (PathProject) or the resource kind (Resource); empty
	// for the other kinds.
	arg string
}

// PathProject marks a route whose project key is the path parameter `param`.
func PathProject(param string) routeScope { return routeScope{kind: scopePathProject, arg: param} }

// Resource marks a route whose project comes from the addressed record of the given kind
// (job, plan, todo, schedule, workflow, session, work_item, tracker_repo, memory, worker …):
// the record carries (or resolves to) its project.
func Resource(kind string) routeScope { return routeScope{kind: scopeResource, arg: kind} }

// BodyProject marks a route whose project (or the record naming it) is in the request
// body, so only the handler can check it, after binding.
func BodyProject() routeScope { return routeScope{kind: scopeBodyProject} }

// Filter marks a list / aggregate route: what it returns must be narrowed to the scope.
func Filter() routeScope { return routeScope{kind: scopeFilter} }

// NoProject marks a route with no project dimension, explicitly: server-global admin /
// meta / self routes. It is a statement, not an omission.
func NoProject() routeScope { return routeScope{kind: scopeNoProject} }

// String renders the scope the way the generated reference shows it.
func (s routeScope) String() string {
	switch s.kind {
	case scopePathProject:
		return "PathProject(" + s.arg + ")"
	case scopeResource:
		return "Resource(" + s.arg + ")"
	case scopeBodyProject:
		return "BodyProject"
	case scopeFilter:
		return "Filter"
	case scopeNoProject:
		return "NoProject"
	}
	return ""
}

// rwClass is the read / write class of a route.
type rwClass uint8

const (
	// rwDefault is the zero value: no explicit override, the HTTP method decides (GET and
	// HEAD read, everything else write).
	rwDefault rwClass = iota
	// rwRead: the route is safe for a read-only caller (ReadSafe).
	rwRead
	// rwWrite: the route changes state, or opens a data-plane connection that can (Mutating).
	rwWrite
)

// ReadSafe explicitly marks a route a read-only caller may reach although its method is not
// GET: a personal-preference write, or a POST with read semantics. Every use must be on the
// reviewed list of authz_route_dims_test.go.
func ReadSafe() rwClass { return rwRead }

// Mutating explicitly marks a route as a write although its method is GET (a data-plane
// connection that can carry writes). Every use must be on the reviewed list of
// authz_route_dims_test.go.
func Mutating() rwClass { return rwWrite }

// methodIsRead reports whether the method is read by default.
func methodIsRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

// effectiveRW resolves a declaration's read / write class for the route key
// `<METHOD> <path>`: the explicit override when there is one, the method default otherwise.
// It returns rwRead or rwWrite, never rwDefault.
func (d routeDecl) effectiveRW(key string) rwClass {
	if d.rw != rwDefault {
		return d.rw
	}
	method, _, _ := strings.Cut(key, " ")
	if methodIsRead(method) {
		return rwRead
	}
	return rwWrite
}
