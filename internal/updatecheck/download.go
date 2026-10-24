package updatecheck

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

const (
	// downloadTimeout bounds one binary download.
	downloadTimeout = 10 * time.Minute
	// maxBinary caps a downloaded archive or binary.
	maxBinary = 512 << 20
)

// Asset is one platform's binary of the latest checked release (gofer-29gi).
type Asset struct {
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	URL     string `json:"url"`
	// Name is the file the URL serves (a GitHub API URL does not end in it).
	Name string `json:"name,omitempty"`
	// SHA256 of the binary; "" = the source published none, so it is not verified.
	SHA256 string `json:"sha256,omitempty"`
	accept string
}

// archive reports the archive format of the download ("" = a raw binary).
func (a Asset) archive() string {
	name := a.Name
	if name == "" {
		name = a.URL
	}
	switch {
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	case strings.HasSuffix(name, ".tar.gz"), strings.HasSuffix(name, ".tgz"):
		return "tar.gz"
	}
	return ""
}

// AssetFor resolves a platform's binary from the last successful check: the GitHub
// release's archive, or the mirror's url_template.
func (c *Checker) AssetFor(goos, goarch string) (Asset, error) {
	c.mu.Lock()
	st, files, assets := c.status, c.files, c.assets
	c.mu.Unlock()
	if st.Latest == "" || st.Error != "" {
		return Asset{}, errors.New("no successful update check yet")
	}
	a := Asset{OS: goos, Arch: goarch, Version: st.Latest, SHA256: files[BinaryName(goos, goarch)]}
	if st.Source == sourceMirror {
		t := strings.TrimSpace(c.cfg().URLTemplate)
		if t == "" {
			return Asset{}, errors.New("server.update.url_template is not configured")
		}
		a.URL = expand(t, st.Latest, goos, goarch)
	} else if name := assetFor(assets, goos, goarch); name != "" {
		a.Name = name
		a.URL, a.accept = assets[name].downloadURL()
	}
	if a.URL == "" {
		return Asset{}, fmt.Errorf("release %s has no binary for %s/%s", st.Latest, goos, goarch)
	}
	return a, nil
}

// AssetForVersion resolves a platform's binary of ONE release (gofer-n1vj): "" or the
// latest checked release is what AssetFor returns; another version is resolved from the
// source itself — the mirror's url_template with that {version}, or that GitHub release's
// assets. A mirror publishes the checksums of its latest release only, so any other
// version is downloaded unverified (the URL is the administrator's own template).
func (c *Checker) AssetForVersion(ctx context.Context, goos, goarch, version string) (Asset, error) {
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if version == "" {
		return c.AssetFor(goos, goarch)
	}
	c.mu.Lock()
	latest := c.status.Latest
	c.mu.Unlock()
	if version == latest {
		return c.AssetFor(goos, goarch)
	}
	u := c.cfg()
	if u.UsesMirror() {
		t := strings.TrimSpace(u.URLTemplate)
		if t == "" {
			return Asset{}, errors.New("server.update.url_template is not configured, so the mirror cannot name a release binary")
		}
		return Asset{OS: goos, Arch: goarch, Version: version, URL: expand(t, version, goos, goarch)}, nil
	}
	return c.gitHubAssetFor(ctx, u, goos, goarch, version)
}

// gitHubAssetFor resolves another release of the GitHub source: that release's archive
// for the platform, plus the checksums of the latest.yaml it ships.
func (c *Checker) gitHubAssetFor(ctx context.Context, u config.UpdateConfig, goos, goarch, version string) (Asset, error) {
	body, err := c.get(ctx, u, githubAPI+"/repos/"+u.EffectiveRepo()+"/releases/tags/v"+version, "application/vnd.github+json")
	if err != nil {
		return Asset{}, err
	}
	var gh struct {
		Assets []struct {
			Name    string `json:"name"`
			Browser string `json:"browser_download_url"`
			API     string `json:"url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &gh); err != nil {
		return Asset{}, fmt.Errorf("read the GitHub release: %v", err)
	}
	assets := make(map[string]ghAsset, len(gh.Assets))
	for _, a := range gh.Assets {
		assets[a.Name] = ghAsset{Browser: a.Browser, API: a.API}
	}
	name := assetFor(assets, goos, goarch)
	if name == "" {
		return Asset{}, fmt.Errorf("release %s has no binary for %s/%s", version, goos, goarch)
	}
	a := Asset{OS: goos, Arch: goarch, Version: version, Name: name}
	a.URL, a.accept = assets[name].downloadURL()
	if meta, ok := assets["latest.yaml"]; ok {
		url, accept := meta.downloadURL()
		if body, err := c.get(ctx, u, url, accept); err == nil {
			if m, err := parseLatest(body); err == nil {
				a.SHA256 = m.Files[BinaryName(goos, goarch)]
			}
		}
	}
	return a, nil
}

// Download fetches the asset into dir as BinaryName(os, arch), extracting it from an
// archive and verifying the published sha256. The result is executable.
func (c *Checker) Download(ctx context.Context, a Asset, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", AppName+"/"+c.current)
	if a.accept != "" {
		req.Header.Set("Accept", a.accept)
	}
	client := c.httpClient(c.cfg())
	client.Timeout = 0 // the context bounds the download
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", a.URL, resp.Status)
	}

	name := BinaryName(a.OS, a.Arch)
	dst := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, name+".*.part")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	body := io.LimitReader(resp.Body, maxBinary+1)
	switch a.archive() {
	case "":
		err = copyBinary(tmp, body)
	case "zip":
		err = extractZip(tmp, body, dir, memberNames(a.OS, name)...)
	default:
		err = extractTarGz(tmp, body, memberNames(a.OS, name)...)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if err := verifySHA256(tmp.Name(), a.SHA256); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	_ = os.Remove(dst)
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", err
	}
	return dst, nil
}

// memberNames lists the archive members that hold the binary: the plain "gofer"
// ("gofer.exe") of current archives, or the platform-suffixed name older archives used.
func memberNames(goos, platformName string) []string {
	plain := AppName
	if goos == "windows" {
		plain += ".exe"
	}
	return []string{plain, platformName}
}

func hasName(names []string, base string) bool {
	for _, n := range names {
		if n == base {
			return true
		}
	}
	return false
}

func copyBinary(dst io.Writer, src io.Reader) error {
	n, err := io.Copy(dst, src)
	if err != nil {
		return err
	}
	if n > maxBinary {
		return errors.New("the download is too large")
	}
	if n == 0 {
		return errors.New("the download is empty")
	}
	return nil
}

// extractTarGz copies the archive member named one of names (at any depth) into dst.
func extractTarGz(dst io.Writer, src io.Reader, names ...string) error {
	gz, err := gzip.NewReader(src)
	if err != nil {
		return fmt.Errorf("read the archive: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("the archive has no %s", names[0])
		}
		if err != nil {
			return fmt.Errorf("read the archive: %v", err)
		}
		if h.Typeflag == tar.TypeReg && hasName(names, path.Base(h.Name)) {
			return copyBinary(dst, io.LimitReader(tr, maxBinary+1))
		}
	}
}

// extractZip copies the member named one of names into dst. zip needs random access, so the
// archive is spooled to a temp file in dir first.
func extractZip(dst io.Writer, src io.Reader, dir string, names ...string) error {
	spool, err := os.CreateTemp(dir, "archive.*.zip")
	if err != nil {
		return err
	}
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()
	size, err := io.Copy(spool, src)
	if err != nil {
		return err
	}
	if size > maxBinary {
		return errors.New("the download is too large")
	}
	zr, err := zip.NewReader(spool, size)
	if err != nil {
		return fmt.Errorf("read the archive: %v", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !hasName(names, path.Base(f.Name)) {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("read the archive: %v", err)
		}
		defer rc.Close()
		return copyBinary(dst, io.LimitReader(rc, maxBinary+1))
	}
	return fmt.Errorf("the archive has no %s", names[0])
}

// verifySHA256 checks file against want; an empty want (nothing published) passes.
func verifySHA256(file, want string) error {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" {
		return nil
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("sha256 mismatch: got %s, want %s", got, want)
	}
	return nil
}
