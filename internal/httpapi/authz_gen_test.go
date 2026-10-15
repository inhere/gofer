package httpapi

import (
	"os"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/authz"
)

// 生成参考文档的兜底测试（设计「权限参考文档由代码生成」，计划 T1.6）：
//
//   - 过期检查：docs/reference/permissions.md 与代码渲染出的内容逐字一致，
//     不一致就是有人改了目录 / 声明表没重跑 `go generate`；
//   - 覆盖检查：目录里的每条权限、声明表里的每条路由都出现在文档里，
//     防止渲染器哪天静默漏行。

// TestAuthzReferenceIsUpToDate is the design's 过期检查: render the document again and
// compare it with the checked-in file. A failure means the file describes an older binary
// than this one — run `go generate ./internal/httpapi/...` and commit the result.
func TestAuthzReferenceIsUpToDate(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(AuthzReferencePath)
	if err != nil {
		t.Fatalf("read %s: %v (run `go generate ./internal/httpapi/...` from the repository root)", AuthzReferencePath, err)
	}
	// The checkout is LF (`.gitattributes`: `* text=auto eol=lf`); normalising anyway keeps
	// a CRLF working copy from failing on anything but real drift.
	got := strings.ReplaceAll(string(raw), "\r\n", "\n")
	want := AuthzReference()
	if got == want {
		return
	}
	gotLines := strings.Split(got, "\n")
	wantLines := strings.Split(want, "\n")
	for i := 0; i < len(gotLines) || i < len(wantLines); i++ {
		var fileLine, wantLine string
		if i < len(gotLines) {
			fileLine = gotLines[i]
		}
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if fileLine != wantLine {
			t.Fatalf("docs/reference/permissions.md is stale at line %d:\n  file:      %s\n  generated: %s\n"+
				"run `go generate ./internal/httpapi/...` and commit the result", i+1, fileLine, wantLine)
		}
	}
	t.Fatalf("docs/reference/permissions.md is stale (the two texts differ only in trailing bytes); " +
		"run `go generate ./internal/httpapi/...` and commit the result")
}

// TestAuthzReferenceCoversCatalogAndRoutes: the document carries every catalog permission
// and every declared route, one row each — the two inputs the design names — so a
// renderer that silently skips rows fails here instead of in review.
func TestAuthzReferenceCoversCatalogAndRoutes(t *testing.T) {
	t.Parallel()
	doc := AuthzReference()
	lines := strings.Split(doc, "\n")

	// One table row per catalog entry, keyed by its first cell (the permission name).
	catalogRows := map[string]bool{}
	// One table row per declaration, keyed by its first two cells (method and path).
	routeRows := map[string]bool{}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		// Both tables are one row per `<METHOD> <path>` / per permission, with no cell
		// spanning a line, so the cells of a data row are enough to key it.
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 2 {
			continue
		}
		perm := strings.Trim(strings.TrimSpace(cells[0]), "`")
		if _, ok := authz.Lookup(perm); ok {
			catalogRows[perm] = true
			continue
		}
		method := strings.Trim(strings.TrimSpace(cells[0]), "`")
		path := strings.Trim(strings.TrimSpace(cells[1]), "`")
		if strings.HasPrefix(path, "/") && method != "" {
			routeRows[method+" "+path] = true
		}
	}

	for _, p := range authz.Permissions() {
		if !catalogRows[p.Name] {
			t.Errorf("the catalog permission %s has no row in the generated document", p.Name)
		}
	}
	if len(catalogRows) != len(authz.Permissions()) {
		t.Errorf("the generated document has %d catalog rows, want %d", len(catalogRows), len(authz.Permissions()))
	}

	for key := range routeDecls {
		if !routeRows[key] {
			t.Errorf("the declared route %s has no row in the generated document", key)
		}
	}
	if len(routeRows) != len(routeDecls) {
		t.Errorf("the generated document has %d route rows, want %d", len(routeRows), len(routeDecls))
	}
}
