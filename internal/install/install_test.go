package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/updatecheck"
)

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The scripts are served to anyone and end up on a machine that is not trusted yet:
// they may name the server and nothing else. A known credential literal and the word
// itself must not appear in either of them.
func TestScriptCarriesNoSecret(t *testing.T) {
	const secret = "gct_9f3a7c2e5b1d4a86"
	for _, name := range []string{ScriptSh, ScriptPS1} {
		body, err := Script(name, "http://10.0.0.5:8767/")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		text := string(body)
		if want := "http://10.0.0.5:8767\n"; !strings.Contains(text, want) &&
			!strings.Contains(text, "'http://10.0.0.5:8767'") && !strings.Contains(text, `"http://10.0.0.5:8767"`) {
			t.Errorf("%s: the server address (without a trailing slash) is not in the script", name)
		}
		if strings.Contains(strings.ToLower(text), "token") {
			t.Errorf("%s: the script mentions a token; it must carry no credential at all", name)
		}
		for _, leak := range []string{secret, "Authorization", "GOFER_WORKER_TOKEN", "gct_"} {
			if strings.Contains(text, leak) {
				t.Errorf("%s: the script leaks %q", name, leak)
			}
		}
		// The template's placeholder must be gone.
		if strings.Contains(text, "{{") {
			t.Errorf("%s: a template placeholder was left unresolved", name)
		}
	}
}

// The rendered script differs per server, and an address that could break out of the
// shell string is refused.
func TestScriptServerAddress(t *testing.T) {
	a, err := Script(ScriptSh, "http://a.example:8767")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Script(ScriptSh, "http://b.example:8767/")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) == string(b) || !strings.Contains(string(a), "a.example") || !strings.Contains(string(b), "b.example") {
		t.Fatal("the server address is not rendered into the script")
	}
	for _, bad := range []string{"", "   ", `http://x/"$(rm -rf /)"`, "http://x/a b"} {
		if _, err := Script(ScriptSh, bad); err == nil {
			t.Errorf("address %q was accepted", bad)
		}
	}
	if _, err := Script("nope.sh", "http://a.example"); err == nil {
		t.Error("an unknown script must fail")
	}
	if got := ContentType(ScriptPS1); !strings.Contains(got, "text/plain") {
		t.Errorf("ps1 content type = %q", got)
	}
	if got := ContentType(ScriptSh); !strings.Contains(got, "shellscript") {
		t.Errorf("sh content type = %q", got)
	}
}

// resolved is a Resolver answering with a fixed asset (or error).
type resolved struct {
	asset updatecheck.Asset
	err   error
	got   struct{ os, arch, version string }
}

func (r *resolved) AssetForVersion(_ context.Context, goos, goarch, version string) (updatecheck.Asset, error) {
	r.got.os, r.got.arch, r.got.version = goos, goarch, version
	return r.asset, r.err
}

// A release build of a platform with a published checksum yields the URL, the file name
// and the sha256 of that binary.
func TestManifestFor(t *testing.T) {
	src := &resolved{asset: updatecheck.Asset{
		OS: "linux", Arch: "amd64", Version: "0.214.1",
		URL: "http://mirror.example/gofer-linux-amd64", SHA256: "ABCDEF0123"}}
	// The build version may carry a dev suffix: the release version is what a source names.
	m, err := ManifestFor(context.Background(), src, Request{Version: "0.214.1-3-gabc1234", OS: "linux", Arch: "amd64", Server: "http://srv:8767/"})
	if err != nil {
		t.Fatal(err)
	}
	if src.got.version != "0.214.1" || src.got.os != "linux" || src.got.arch != "amd64" {
		t.Fatalf("resolved %+v", src.got)
	}
	want := Manifest{Version: "0.214.1", OS: "linux", Arch: "amd64",
		URL: "http://mirror.example/gofer-linux-amd64", Name: "gofer-linux-amd64",
		SHA256: "abcdef0123", Server: "http://srv:8767"}
	if m != want {
		t.Fatalf("manifest = %+v, want %+v", m, want)
	}
}

// An archive asset keeps the asset's own name (a GitHub API URL does not end in it).
func TestManifestForArchiveName(t *testing.T) {
	src := &resolved{asset: updatecheck.Asset{OS: "windows", Arch: "amd64", Version: "0.214.1",
		URL: "http://api.example/assets/3", Name: "gofer-windows-amd64.zip", SHA256: "ab"}}
	m, err := ManifestFor(context.Background(), src, Request{Version: "0.214.1", OS: "windows", Arch: "amd64"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "gofer-windows-amd64.zip" {
		t.Fatalf("name = %q", m.Name)
	}
}

// A source that publishes no checksum cannot be installed from: the manifest refuses
// (the httpapi layer answers 404) instead of offering an unverified download.
func TestManifestRefusesMissingChecksum(t *testing.T) {
	src := &resolved{asset: updatecheck.Asset{OS: "linux", Arch: "amd64", Version: "0.214.1", URL: "http://mirror.example/gofer-linux-amd64"}}
	if _, err := ManifestFor(context.Background(), src, Request{Version: "0.214.1", OS: "linux", Arch: "amd64"}); !errors.Is(err, ErrNoChecksum) {
		t.Fatalf("err = %v, want ErrNoChecksum", err)
	}
	// A dev build names no release at all.
	src = &resolved{}
	if _, err := ManifestFor(context.Background(), src, Request{Version: "dev-abc1234"}); !errors.Is(err, ErrNoReleaseVersion) {
		t.Fatalf("dev build err = %v, want ErrNoReleaseVersion", err)
	}
	// A source failure is passed on as-is (the httpapi layer answers 409).
	boom := errors.New("no successful update check yet")
	if _, err := ManifestFor(context.Background(), &resolved{err: boom}, Request{Version: "0.214.1"}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the source error", err)
	}
}

// A mirror that publishes a per-release latest.yaml is what the installer resolves
// through, checksum included (plan R20): the manifest of the server's own version works
// even when the source's latest release is newer.
func TestManifestFromMirror(t *testing.T) {
	binary := []byte("0.214.1 linux binary")
	sum := sha256Hex(binary)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.yaml":
			_, _ = w.Write([]byte("version: 0.215.0\nfiles:\n  gofer-linux-amd64: " + sum + "\n"))
		case "/0.214.1/latest.yaml":
			_, _ = w.Write([]byte("version: 0.214.1\nfiles:\n  gofer-linux-amd64: " + sum + "\n"))
		case "/0.214.1/gofer-linux-amd64":
			_, _ = w.Write(binary)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u := config.UpdateConfig{LatestURL: srv.URL + "/latest.yaml", URLTemplate: srv.URL + "/{version}/{name}-{os}-{arch}{ext}"}
	c := updatecheck.New("0.214.1", func() config.UpdateConfig { return u })
	c.Check(context.Background())

	m, err := ManifestFor(context.Background(), c, Request{Version: "0.214.1", OS: "linux", Arch: "amd64", Server: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if m.URL != srv.URL+"/0.214.1/gofer-linux-amd64" || m.SHA256 != sum || m.Version != "0.214.1" {
		t.Fatalf("manifest = %+v", m)
	}
}
