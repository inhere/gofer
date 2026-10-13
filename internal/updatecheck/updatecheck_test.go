package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		latest, current string
		want            bool
	}{
		{"0.153.0", "0.152.3", true},
		{"0.152.4", "0.152.3 (66fdbbd)", true},
		{"1.0.0", "0.999.9", true},
		{"0.152.3", "0.152.3", false},
		{"0.152.3", "0.152.3-2-gabc1234", false}, // a dev build past the release
		{"0.152.3", "0.152.3-dirty", false},
		{"0.152.2", "0.152.3", false},
		{"v0.153.0", "v0.152.3", true},
		{"garbage", "0.152.3", false},
		{"0.153.0", "dev", false},
	} {
		if got := Newer(tc.latest, tc.current); got != tc.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tc.latest, tc.current, got, tc.want)
		}
	}
}

func checker(current string, u config.UpdateConfig, goos, goarch string) *Checker {
	c := New(current, func() config.UpdateConfig { return u })
	c.goos, c.goarch = goos, goarch
	return c
}

func TestCheckGitHub(t *testing.T) {
	var gotPath, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAgent = r.URL.Path, r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`{"tag_name":"v0.153.0","html_url":"https://github.com/o/r/releases/tag/v0.153.0","published_at":"2026-10-13T00:00:00Z",
			"assets":[{"name":"gofer-linux-amd64.tar.gz","browser_download_url":"https://dl/linux"},{"name":"gofer-windows-amd64.zip","browser_download_url":"https://dl/windows"}]}`))
	}))
	defer srv.Close()
	old := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = old }()

	c := checker("0.152.3 (66fdbbd)", config.UpdateConfig{Repo: "o/r"}, "windows", "amd64")
	if st := c.Status(); st.CheckedAt != 0 || st.Source != sourceGitHub {
		t.Fatalf("before any check: %+v", st)
	}
	st := c.Check(context.Background())
	if st.Error != "" || !st.Newer || st.Latest != "0.153.0" || st.DownloadURL != "https://dl/windows" ||
		st.ReleaseURL == "" || st.Source != sourceGitHub || st.CheckedAt == 0 {
		t.Fatalf("status = %+v", st)
	}
	if gotPath != "/repos/o/r/releases/latest" || !strings.HasPrefix(gotAgent, "gofer/") {
		t.Fatalf("request path %q agent %q", gotPath, gotAgent)
	}
	if c.Status() != st {
		t.Fatal("the result was not kept")
	}
}

func TestCheckMirror(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tools/gofer/latest.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("name: gofer\nversion: 0.152.3\nreleased_at: 2026-10-13T01:02:03Z\ndescription: a control plane\n"))
	}))
	defer srv.Close()
	u := config.UpdateConfig{LatestURL: srv.URL + "/tools/{name}/latest.yaml", URLTemplate: srv.URL + "/tools/{name}/{name}-{os}-{arch}{ext}"}

	st := checker("0.152.3", u, "windows", "amd64").Check(context.Background())
	if st.Error != "" || st.Newer || st.Latest != "0.152.3" || st.Source != sourceMirror || st.Description != "a control plane" ||
		st.DownloadURL != srv.URL+"/tools/gofer/gofer-windows-amd64.exe" {
		t.Fatalf("windows status = %+v", st)
	}
	st = checker("0.150.0", u, "linux", "arm64").Check(context.Background())
	if !st.Newer || st.DownloadURL != srv.URL+"/tools/gofer/gofer-linux-arm64" {
		t.Fatalf("linux status = %+v", st)
	}
}

// A failing source is reported in Error, never as "newer".
func TestCheckFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer srv.Close()
	st := checker("0.152.3", config.UpdateConfig{LatestURL: srv.URL + "/latest.yaml"}, "linux", "amd64").Check(context.Background())
	if st.Error == "" || st.Newer || st.CheckedAt == 0 {
		t.Fatalf("status = %+v", st)
	}
}

// The configured proxy carries the request.
func TestCheckUsesProxy(t *testing.T) {
	var proxied bool
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied = true
		_, _ = w.Write([]byte("version: 0.200.0\n"))
	}))
	defer proxy.Close()
	u := config.UpdateConfig{LatestURL: "http://mirror.invalid/tools/{name}/latest.yaml", Proxy: proxy.URL}
	st := checker("0.152.3", u, "linux", "amd64").Check(context.Background())
	if !proxied || st.Error != "" || st.Latest != "0.200.0" {
		t.Fatalf("proxied=%v status=%+v", proxied, st)
	}
}
