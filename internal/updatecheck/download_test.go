package updatecheck

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(body)
	_ = zw.Close()
	return buf.Bytes()
}

// The mirror publishes raw binaries plus their sha256 in latest.yaml; every platform
// resolves from the one check, and a wrong checksum is refused.
func TestMirrorAssetDownload(t *testing.T) {
	linux, windows := []byte("linux binary"), []byte("windows binary")
	files := map[string][]byte{"gofer-linux-amd64": linux, "gofer-windows-amd64.exe": windows}
	badSum := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/latest.yaml" {
			ws := sum(windows)
			if badSum {
				ws = sum([]byte("other"))
			}
			_, _ = w.Write([]byte("name: gofer\nversion: 0.160.0\nfiles:\n  gofer-linux-amd64: " + sum(linux) + "\n  gofer-windows-amd64.exe: " + ws + "\n"))
			return
		}
		if b, ok := files[strings.TrimPrefix(r.URL.Path, "/0.160.0/")]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	u := config.UpdateConfig{LatestURL: srv.URL + "/latest.yaml", URLTemplate: srv.URL + "/{version}/{name}-{os}-{arch}{ext}"}
	c := checker("0.153.1", u, "linux", "amd64")
	if st := c.Check(context.Background()); st.SHA256 != sum(linux) {
		t.Fatalf("status sha256 = %q", st.SHA256)
	}
	a, err := c.AssetFor("windows", "amd64")
	if err != nil || a.URL != srv.URL+"/0.160.0/gofer-windows-amd64.exe" || a.SHA256 != sum(windows) || a.Version != "0.160.0" {
		t.Fatalf("asset = %+v, %v", a, err)
	}
	dir := t.TempDir()
	p, err := c.Download(context.Background(), a, dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); !bytes.Equal(got, windows) || filepath.Base(p) != "gofer-windows-amd64.exe" {
		t.Fatalf("downloaded %s = %q", p, got)
	}
	if _, err := c.AssetFor("darwin", "arm64"); err != nil {
		t.Fatalf("an unlisted platform still resolves from the template: %v", err)
	}

	badSum = true
	c.Check(context.Background())
	a, _ = c.AssetFor("windows", "amd64")
	if _, err := c.Download(context.Background(), a, t.TempDir()); err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("a wrong checksum: %v", err)
	}
}

// A GitHub release ships archives plus latest.yaml; the binary is extracted from the
// platform's archive and verified against latest.yaml.
func TestGitHubAssetDownload(t *testing.T) {
	linux, windows := []byte("linux binary"), []byte("windows binary")
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/o/r/releases/latest" {
			_, _ = w.Write([]byte(`{"tag_name":"v0.160.0","assets":[
				{"name":"gofer-linux-amd64.tar.gz","browser_download_url":"https://github.example/linux","url":"` + base + `/assets/1"},
				{"name":"gofer-windows-amd64.zip","browser_download_url":"https://github.example/windows","url":"` + base + `/assets/2"},
				{"name":"latest.yaml","browser_download_url":"https://github.example/latest","url":"` + base + `/assets/3"}]}`))
			return
		}
		// The server downloads through the API asset URLs, which need this Accept.
		if r.Header.Get("Accept") != "application/octet-stream" {
			http.Error(w, "want octet-stream", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/assets/3":
			_, _ = w.Write([]byte("version: 0.160.0\ndescription: d\nfiles:\n  gofer-linux-amd64: " + sum(linux) + "\n  gofer-windows-amd64.exe: " + sum(windows) + "\n"))
		case "/assets/1":
			_, _ = w.Write(tarGz(t, "gofer-linux-amd64", linux))
		case "/assets/2":
			_, _ = w.Write(zipOf(t, "gofer-windows-amd64.exe", windows))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	base = srv.URL
	old := githubAPI
	githubAPI = srv.URL
	defer func() { githubAPI = old }()

	c := checker("0.153.1", config.UpdateConfig{Repo: "o/r"}, "linux", "amd64")
	st := c.Check(context.Background())
	if st.Error != "" || st.SHA256 != sum(linux) || st.Description != "d" || st.DownloadURL != "https://github.example/linux" {
		t.Fatalf("status = %+v", st)
	}
	for _, tc := range []struct {
		goos, arch string
		want       []byte
	}{{"linux", "amd64", linux}, {"windows", "amd64", windows}} {
		a, err := c.AssetFor(tc.goos, tc.arch)
		if err != nil {
			t.Fatal(err)
		}
		p, err := c.Download(context.Background(), a, t.TempDir())
		if err != nil {
			t.Fatalf("%s: %v", tc.goos, err)
		}
		if got, _ := os.ReadFile(p); !bytes.Equal(got, tc.want) {
			t.Fatalf("%s: %q", tc.goos, got)
		}
	}
	if _, err := c.AssetFor("darwin", "arm64"); err == nil {
		t.Fatal("a platform without an asset must fail")
	}
}

func TestAssetForBeforeCheck(t *testing.T) {
	if _, err := checker("0.1.0", config.UpdateConfig{}, "linux", "amd64").AssetFor("linux", "amd64"); err == nil {
		t.Fatal("no check yet must fail")
	}
}

// The callback (update.available) fires once per newly seen newer version.
func TestOnAvailableOncePerVersion(t *testing.T) {
	version := "0.160.0"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("version: " + version + "\n"))
	}))
	defer srv.Close()
	c := checker("0.153.1", config.UpdateConfig{LatestURL: srv.URL}, "linux", "amd64")
	var got []string
	c.SetOnAvailable(func(st Status) { got = append(got, st.Latest) })
	c.Check(context.Background())
	c.Check(context.Background())
	version = "0.161.0"
	c.Check(context.Background())
	if strings.Join(got, ",") != "0.160.0,0.161.0" {
		t.Fatalf("notified %v", got)
	}
}

// Release archives hold a plain gofer / gofer.exe; archives from older releases hold
// the platform-suffixed name. Both must extract, so existing installs can still update.
func TestExtractArchiveLayouts(t *testing.T) {
	body := []byte("binary")
	for _, tc := range []struct {
		goos, member string
	}{
		{"linux", "gofer"}, {"linux", "gofer-linux-amd64"}, {"linux", "dir/gofer"},
		{"windows", "gofer.exe"}, {"windows", "gofer-windows-amd64.exe"},
	} {
		names := memberNames(tc.goos, BinaryName(tc.goos, "amd64"))
		var out bytes.Buffer
		var err error
		if tc.goos == "windows" {
			err = extractZip(&out, bytes.NewReader(zipOf(t, tc.member, body)), t.TempDir(), names...)
		} else {
			err = extractTarGz(&out, bytes.NewReader(tarGz(t, tc.member, body)), names...)
		}
		if err != nil || !bytes.Equal(out.Bytes(), body) {
			t.Errorf("%s %s: %v %q", tc.goos, tc.member, err, out.Bytes())
		}
	}
	var out bytes.Buffer
	if err := extractTarGz(&out, bytes.NewReader(tarGz(t, "README", body)), memberNames("linux", "gofer-linux-amd64")...); err == nil {
		t.Error("an archive without the binary must fail")
	}
}
