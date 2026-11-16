package httpapi

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/updatecheck"
)

// installTestServer is a server that can answer the installer routes: one configurable
// release mirror and a build version.
func installTestServer(t *testing.T, webBaseURL string) *Server {
	t.Helper()
	return newCredentialServer(t, config.ServerConfig{
		Callers:    []config.CallerConfig{{ID: "alice", Token: "tok-user"}},
		WebBaseURL: webBaseURL,
	}, map[string]config.AgentConfig{"exec": {Type: agent.TypeExec}}, nil)
}

// The installers are public and read-only: a machine that is not a worker yet holds no
// credential, so these routes must answer without one — and their content is the
// server's address, never a credential.
func TestInstallScriptsArePublic(t *testing.T) {
	t.Parallel()
	s := installTestServer(t, "")
	for _, path := range []string{"/install.sh", "/install.ps1"} {
		resp := do(t, s, http.MethodGet, path, "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s without a token: status=%d", path, resp.StatusCode)
		}
		body := readBody(t, resp)
		// No server.web_base_url: the address of the request itself.
		if !strings.Contains(body, "http://example.com") {
			t.Errorf("%s does not name the request's host: %s", path, firstLines(body, 6))
		}
		if strings.Contains(strings.ToLower(body), "token") || strings.Contains(body, testToken) {
			t.Errorf("%s carries a credential", path)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/") {
			t.Errorf("%s content type = %q", path, ct)
		}
	}
}

// server.web_base_url wins over the request's own host: that is the address a browser and
// the pairing flow use.
func TestInstallScriptsUseWebBaseURL(t *testing.T) {
	t.Parallel()
	s := installTestServer(t, " https://gofer.example.com/ ")
	resp := do(t, s, http.MethodGet, "/install.sh", "", nil)
	body := readBody(t, resp)
	if !strings.Contains(body, "https://gofer.example.com") || strings.Contains(body, "http://example.com") {
		t.Fatalf("script does not use server.web_base_url: %s", firstLines(body, 8))
	}
}

// The manifest names this server's version for the platform, with the sha256 the update
// source publishes — no token needed, nothing else revealed.
func TestInstallManifest(t *testing.T) {
	t.Parallel()
	bins := map[string][]byte{
		updatecheck.BinaryName("linux", "amd64"):             []byte("0.214.1 linux binary"),
		updatecheck.BinaryName("darwin", "arm64"):            []byte("0.214.1 darwin binary"),
		updatecheck.BinaryName(runtime.GOOS, runtime.GOARCH): []byte("0.214.1 own binary"),
	}
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/latest.yaml", r.URL.Path == "/0.214.1/latest.yaml":
			var b strings.Builder
			b.WriteString("version: 0.214.1\nfiles:\n")
			for name, body := range bins {
				b.WriteString("  " + name + ": " + hexSum(body) + "\n")
			}
			_, _ = w.Write([]byte(b.String()))
		default:
			if body, ok := bins[strings.TrimPrefix(r.URL.Path, "/0.214.1/")]; ok {
				_, _ = w.Write(body)
				return
			}
			http.NotFound(w, r)
		}
	}))
	defer mirror.Close()

	s := installTestServer(t, "")
	s.SetBuildInfo(buildinfo.Info{Version: "0.214.1", GitCommit: "abc1234"})
	u := config.UpdateConfig{LatestURL: mirror.URL + "/latest.yaml", URLTemplate: mirror.URL + "/{version}/{name}-{os}-{arch}{ext}"}
	s.SetUpdateChecker(updatecheck.New("0.214.1", func() config.UpdateConfig { return u }))

	linux := bins[updatecheck.BinaryName("linux", "amd64")]
	resp := do(t, s, http.MethodGet, "/install/manifest?os=linux&arch=amd64", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d: %s", resp.StatusCode, readBody(t, resp))
	}
	var m struct {
		Version, OS, Arch, URL, Name, SHA256, Server string
	}
	decode(t, resp, &m)
	if m.Version != "0.214.1" || m.OS != "linux" || m.Arch != "amd64" ||
		m.URL != mirror.URL+"/0.214.1/gofer-linux-amd64" || m.Name != "gofer-linux-amd64" || m.SHA256 != hexSum(linux) {
		t.Fatalf("manifest = %+v", m)
	}
	if m.Server != "http://example.com" {
		t.Fatalf("server = %q, want the request's host", m.Server)
	}

	// A platform of the same release resolves through the same template.
	resp = do(t, s, http.MethodGet, "/install/manifest?os=darwin&arch=arm64", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("darwin status=%d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	// An incomplete request defaults to the server's own platform.
	resp = do(t, s, http.MethodGet, "/install/manifest", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("default platform status=%d: %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()
}

// A source with no checksum for the binary means no verifiable install: 404 with the
// reason, so the script stops instead of installing blind.
func TestInstallManifestMissingChecksum(t *testing.T) {
	t.Parallel()
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.yaml" {
			_, _ = w.Write([]byte("version: 0.214.1\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer mirror.Close()

	s := installTestServer(t, "")
	s.SetBuildInfo(buildinfo.Info{Version: "0.214.1"})
	u := config.UpdateConfig{LatestURL: mirror.URL + "/latest.yaml", URLTemplate: mirror.URL + "/{name}-{os}-{arch}{ext}"}
	s.SetUpdateChecker(updatecheck.New("0.214.1", func() config.UpdateConfig { return u }))

	resp := do(t, s, http.MethodGet, "/install/manifest?os=linux&arch=amd64", "", nil)
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404: %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "sha256") {
		t.Fatalf("the answer does not say what is missing: %s", body)
	}
}

// Without a release source nothing can be named (503), and a build that is not a release
// has no version to download (404).
func TestInstallManifestUnavailable(t *testing.T) {
	t.Parallel()
	s := installTestServer(t, "")
	resp := do(t, s, http.MethodGet, "/install/manifest", "", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no checker status=%d, want 503", resp.StatusCode)
	}
	resp.Body.Close()

	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: 0.214.1\nfiles:\n  gofer-linux-amd64: abc\n"))
	}))
	defer mirror.Close()
	s.SetUpdateChecker(updatecheck.New("dev-abc1234", func() config.UpdateConfig {
		return config.UpdateConfig{LatestURL: mirror.URL + "/latest.yaml"}
	}))
	s.SetBuildInfo(buildinfo.Info{Version: "dev-abc1234"})
	resp = do(t, s, http.MethodGet, "/install/manifest", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("dev build status=%d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// firstLines keeps a failure message readable: a whole installer in the log is noise.
func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
