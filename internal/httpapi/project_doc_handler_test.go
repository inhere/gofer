package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/project"
)

// docServer builds a browse server over a project root holding a spread of docs.
// It returns the server and the project root (to assert no path leaks).
func docServer(t *testing.T) (*Server, string) {
	t.Helper()
	repo := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("docs/a.md", "# a\n")
	write("sub/docs/b.md", "# sub b\n")
	write(".env.md", "SECRET\n")
	write("x.txt", "SECRET\n")
	write("bin.md", "ab\x00cd")
	return newBrowseServer(t, repo), repo
}

func TestHandleProjectDoc_OK(t *testing.T) {
	t.Parallel()
	s, _ := docServer(t)

	resp := do(t, s, http.MethodGet, "/v1/projects/self/doc?path=docs/a.md", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d, want 200", resp.StatusCode)
	}
	var dc project.DocContent
	decode(t, resp, &dc)
	if dc.Path != "docs/a.md" || !strings.Contains(dc.Content, "# a") || dc.Truncated {
		t.Fatalf("unexpected doc: %+v", dc)
	}

	// base hit, then base miss falling back to the project root.
	resp = do(t, s, http.MethodGet, "/v1/projects/self/doc?path=docs/b.md&base=sub", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("base hit status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &dc)
	if dc.Path != "sub/docs/b.md" {
		t.Fatalf("base hit path=%q", dc.Path)
	}
	resp = do(t, s, http.MethodGet, "/v1/projects/self/doc?path=docs/a.md&base=sub", testToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("base fallback status=%d, want 200", resp.StatusCode)
	}
	decode(t, resp, &dc)
	if dc.Path != "docs/a.md" {
		t.Fatalf("base fallback path=%q", dc.Path)
	}
}

func TestHandleProjectDoc_Errors(t *testing.T) {
	t.Parallel()
	s, repo := docServer(t)
	top := filepath.Dir(repo)
	tests := []struct {
		name, url string
		status    int
	}{
		{"missing path", "/v1/projects/self/doc", http.StatusBadRequest},
		{"unknown project", "/v1/projects/nope/doc?path=docs/a.md", http.StatusNotFound},
		{"hidden file", "/v1/projects/self/doc?path=.env.md", http.StatusForbidden},
		{"txt ext", "/v1/projects/self/doc?path=x.txt", http.StatusForbidden},
		{"ads colon", "/v1/projects/self/doc?path=secret:x.md", http.StatusForbidden},
		{"dotdot", "/v1/projects/self/doc?path=../a.md", http.StatusForbidden},
		{"hidden base", "/v1/projects/self/doc?path=a.md&base=.git", http.StatusForbidden},
		{"not found", "/v1/projects/self/doc?path=docs/none.md", http.StatusNotFound},
		{"binary", "/v1/projects/self/doc?path=bin.md", http.StatusUnsupportedMediaType},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := do(t, s, http.MethodGet, tc.url, testToken, nil)
			body := readBody(t, resp)
			if resp.StatusCode != tc.status {
				t.Fatalf("status=%d, want %d (%s)", resp.StatusCode, tc.status, body)
			}
			if strings.Contains(body, repo) || strings.Contains(body, top) || strings.Contains(body, "SECRET") {
				t.Fatalf("response leaks a path or content: %s", body)
			}
		})
	}
}

func TestHandleProjectDoc_NotLocal409(t *testing.T) {
	t.Parallel()
	s, repo := docServer(t)
	// A project that runs only on a worker: the server must not read same-named local files.
	cfg := s.projects.Config()
	p := cfg.Projects["self"]
	p.AllowedRunners = []string{"w1"}
	cfg.Projects["remote"] = p

	resp := do(t, s, http.MethodGet, "/v1/projects/remote/doc?path=docs/a.md", testToken, nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, "project_not_local") {
		t.Fatalf("status=%d body=%s, want 409 project_not_local", resp.StatusCode, body)
	}
	if strings.Contains(body, repo) || strings.Contains(body, "# a") {
		t.Fatalf("409 leaks: %s", body)
	}
}

func TestHandleProjectDoc_CeilingHidesProject(t *testing.T) {
	s := newCeilingServer(t)
	grantAll(s)
	root := s.projects.Config().Storage.Root
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "a.md"), []byte("# a\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	status, body := ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/projects/p1/doc?path=docs/a.md", nil)
	if status != http.StatusOK {
		t.Fatalf("in-scope status=%d (%s), want 200", status, body)
	}
	status, body = ceilReq(t, s, ceilTokLim, http.MethodGet, "/v1/projects/p2/doc?path=docs/a.md", nil)
	if status != http.StatusNotFound || strings.Contains(body, "# a") {
		t.Fatalf("out-of-ceiling status=%d (%s), want 404 without content", status, body)
	}
	status, _ = ceilReq(t, s, ceilTokPlain, http.MethodGet, "/v1/projects/p2/doc?path=docs/a.md", nil)
	if status != http.StatusOK {
		t.Fatalf("unrestricted caller status=%d, want 200", status)
	}
}
