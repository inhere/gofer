// Package updatecheck looks for a newer gofer release (gofer-6x2n,
// docs/design/2026-10-13-update-check-design.md): the latest GitHub release by
// default, or a mirror's latest.yaml. Download (gofer-29gi) fetches and verifies a
// platform's binary for an upgrade; installing it is the caller's business.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/config"
)

// AppName is the {name} of the release artifacts.
const AppName = "gofer"

const (
	sourceGitHub = "github"
	sourceMirror = "mirror"
	// checkTimeout bounds one check.
	checkTimeout = 30 * time.Second
	// firstCheckDelay lets the server settle before the first periodic check.
	firstCheckDelay = time.Minute
	// maxBody caps the response read from a source.
	maxBody = 1 << 20
)

// githubAPI is the GitHub API base (a var for tests).
var githubAPI = "https://api.github.com"

// Status is the result of the latest check.
type Status struct {
	Current     string `json:"current"`
	Latest      string `json:"latest,omitempty"`
	Newer       bool   `json:"newer"`
	Source      string `json:"source"`
	CheckedAt   int64  `json:"checked_at"`
	ReleasedAt  string `json:"released_at,omitempty"`
	ReleaseURL  string `json:"release_url,omitempty"`
	DownloadURL string `json:"download_url,omitempty"`
	// SHA256 is the published checksum of this platform's binary ("" = the source
	// published none: latest.yaml without files, or a release without latest.yaml).
	SHA256      string `json:"sha256,omitempty"`
	Description string `json:"description,omitempty"`
	Error       string `json:"error,omitempty"`
}

// Checker checks one source and keeps the latest result.
type Checker struct {
	current string
	cfg     func() config.UpdateConfig
	now     func() time.Time
	goos    string
	goarch  string

	mu       sync.Mutex
	status   Status
	notified string // the latest version already logged as available
	// files / assets describe the latest release for every platform (binary name ->
	// sha256; GitHub asset name -> its URLs), for a cross-platform worker upgrade.
	files       map[string]string
	assets      map[string]ghAsset
	onAvailable func(Status)
}

// SetOnAvailable registers a callback run once per newly seen newer version (the
// update.available notification).
func (c *Checker) SetOnAvailable(fn func(Status)) {
	c.mu.Lock()
	c.onAvailable = fn
	c.mu.Unlock()
}

// BinaryName is the release binary of a platform: gofer-<os>-<arch>[.exe].
func BinaryName(goos, goarch string) string {
	name := AppName + "-" + goos + "-" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// New builds a checker for the running version; cfg is read on every check, so a
// reload applies to the next one.
func New(current string, cfg func() config.UpdateConfig) *Checker {
	c := &Checker{current: strings.TrimSpace(current), cfg: cfg, now: time.Now, goos: runtime.GOOS, goarch: runtime.GOARCH}
	c.status = Status{Current: c.current, Source: sourceOf(cfg())}
	return c
}

func sourceOf(u config.UpdateConfig) string {
	if u.UsesMirror() {
		return sourceMirror
	}
	return sourceGitHub
}

// Status returns the latest result (CheckedAt 0 = never checked).
func (c *Checker) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Check runs one check now and returns (and keeps) its result. A failure is in
// Status.Error, never fatal.
func (c *Checker) Check(ctx context.Context) Status {
	u := c.cfg()
	st := Status{Current: c.current, Source: sourceOf(u), CheckedAt: c.now().Unix()}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	var err error
	rel := release{}
	if u.UsesMirror() {
		rel, err = c.checkMirror(ctx, u, &st)
	} else {
		rel, err = c.checkGitHub(ctx, u, &st)
	}
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Newer = Newer(st.Latest, c.current)
		st.SHA256 = rel.files[BinaryName(c.goos, c.goarch)]
	}
	c.mu.Lock()
	c.status = st
	if err == nil {
		c.files, c.assets = rel.files, rel.assets
	}
	announce := st.Newer && st.Latest != c.notified
	if announce {
		c.notified = st.Latest
	}
	notify := c.onAvailable
	c.mu.Unlock()
	if announce {
		slog.Info("update.available", "event", "update.available", "current", c.current, "latest", st.Latest, "source", st.Source)
		if notify != nil {
			notify(st)
		}
	}
	return st
}

// Run checks after firstCheckDelay and then every interval until ctx ends; a
// disabled config skips the periodic checks (and is re-read each time).
func (c *Checker) Run(ctx context.Context) {
	wait := firstCheckDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		u := c.cfg()
		if u.IsEnabled() {
			c.Check(ctx)
		}
		wait = u.EffectiveInterval()
	}
}

func (c *Checker) httpClient(u config.UpdateConfig) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if p := strings.TrimSpace(u.Proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{Transport: tr, Timeout: checkTimeout}
}

func (c *Checker) get(ctx context.Context, u config.UpdateConfig, target string, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	req.Header.Set("User-Agent", AppName+"/"+c.current)
	resp, err := c.httpClient(u).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", target, resp.Status)
	}
	return body, nil
}

// release is what a check learned about every platform of the latest release.
type release struct {
	files  map[string]string  // binary name -> sha256
	assets map[string]ghAsset // GitHub asset name -> its URLs
}

// ghAsset is a GitHub release asset. Browser is the github.com link shown to a
// person; API is the api.github.com URL the server downloads from (with Accept:
// application/octet-stream) — some networks reach the API but time out on
// github.com download links.
type ghAsset struct {
	Browser string
	API     string
}

const octetStream = "application/octet-stream"

// downloadURL is the URL the server fetches the asset from, and the Accept it needs.
func (a ghAsset) downloadURL() (string, string) {
	if a.API != "" {
		return a.API, octetStream
	}
	return a.Browser, ""
}

// latestMeta is latest.yaml (make latest-yaml): the mirror's index, and an asset of
// a GitHub release.
type latestMeta struct {
	Name        string            `yaml:"name"`
	Version     string            `yaml:"version"`
	ReleasedAt  string            `yaml:"released_at"`
	Description string            `yaml:"description"`
	Files       map[string]string `yaml:"files"`
}

func parseLatest(body []byte) (latestMeta, error) {
	var meta latestMeta
	if err := yaml.Unmarshal(body, &meta); err != nil {
		return meta, fmt.Errorf("read latest.yaml: %v", err)
	}
	return meta, nil
}

func (c *Checker) checkGitHub(ctx context.Context, u config.UpdateConfig, st *Status) (release, error) {
	rel := release{assets: map[string]ghAsset{}}
	body, err := c.get(ctx, u, githubAPI+"/repos/"+u.EffectiveRepo()+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return rel, err
	}
	var gh struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name    string `json:"name"`
			Browser string `json:"browser_download_url"`
			API     string `json:"url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &gh); err != nil {
		return rel, fmt.Errorf("read the GitHub release: %v", err)
	}
	if gh.TagName == "" {
		return rel, errors.New("the GitHub release has no tag")
	}
	st.Latest = strings.TrimPrefix(gh.TagName, "v")
	st.ReleaseURL, st.ReleasedAt = gh.HTMLURL, gh.PublishedAt
	for _, a := range gh.Assets {
		rel.assets[a.Name] = ghAsset{Browser: a.Browser, API: a.API}
	}
	if name := assetFor(rel.assets, c.goos, c.goarch); name != "" {
		st.DownloadURL = rel.assets[name].Browser
	}
	// The release's latest.yaml carries the binaries' checksums; an older release
	// without it simply has none.
	if a, ok := rel.assets["latest.yaml"]; ok {
		url, accept := a.downloadURL()
		body, err := c.get(ctx, u, url, accept)
		if err == nil {
			var meta latestMeta
			if meta, err = parseLatest(body); err == nil {
				rel.files = meta.Files
				if st.Description == "" {
					st.Description = meta.Description
				}
			}
		}
		if err != nil {
			slog.Warn("update.checksums_unavailable", "event", "update.checksums_unavailable", "latest", st.Latest, "error", err)
		}
	}
	return rel, nil
}

// assetFor is the name of a platform's GitHub asset (an archive).
func assetFor(assets map[string]ghAsset, goos, goarch string) string {
	prefix := AppName + "-" + goos + "-" + goarch + "."
	for name := range assets {
		if strings.HasPrefix(name, prefix) && (strings.HasSuffix(name, ".tar.gz") || strings.HasSuffix(name, ".zip")) {
			return name
		}
	}
	return ""
}

func (c *Checker) checkMirror(ctx context.Context, u config.UpdateConfig, st *Status) (release, error) {
	body, err := c.get(ctx, u, expand(u.LatestURL, "", c.goos, c.goarch), "")
	if err != nil {
		return release{}, err
	}
	meta, err := parseLatest(body)
	if err != nil {
		return release{}, err
	}
	if strings.TrimSpace(meta.Version) == "" {
		return release{}, errors.New("latest.yaml has no version")
	}
	st.Latest = strings.TrimPrefix(strings.TrimSpace(meta.Version), "v")
	st.ReleasedAt, st.Description = meta.ReleasedAt, meta.Description
	if t := strings.TrimSpace(u.URLTemplate); t != "" {
		st.DownloadURL = expand(t, st.Latest, c.goos, c.goarch)
	}
	return release{files: meta.Files}, nil
}

// expand fills the {name} {os} {arch} {ext} {version} placeholders.
func expand(tmpl, version, goos, goarch string) string {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	return strings.NewReplacer("{name}", AppName, "{os}", goos, "{arch}", goarch, "{ext}", ext, "{version}", version).Replace(strings.TrimSpace(tmpl))
}

// Newer reports whether latest is a newer release than current. Versions compare as
// X.Y.Z numbers; a development build of current ("0.152.3-2-gabc", "-dirty") counts
// as past its base release, so an equal base is not "newer". An unparsable version
// never reports newer.
func Newer(latest, current string) bool {
	lv, ok1 := parseVersion(latest)
	cv, ok2 := parseVersion(current)
	if !ok1 || !ok2 {
		return false
	}
	for i := range lv {
		if lv[i] != cv[i] {
			return lv[i] > cv[i]
		}
	}
	// Equal base: a release is never newer than itself, nor than a build past it.
	return false
}

// ReleaseVersion turns a build version into the release version a download source names
// (gofer-n1vj): "0.152.3" for "0.152.3", "v0.152.3", "0.152.3-2-gabc1234" or
// "0.152.3 (abc1234)"; "" when the build is not a release version ("dev", a bare commit).
func ReleaseVersion(build string) string {
	if _, ok := parseVersion(build); !ok {
		return ""
	}
	v := strings.TrimPrefix(strings.TrimSpace(build), "v")
	if i := strings.IndexAny(v, " ("); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	return v
}

// parseVersion reads the X.Y.Z of "vX.Y.Z[-suffix]" (the display form
// "X.Y.Z (commit)" too); a suffix (dev build) does not change the base.
func parseVersion(v string) (nums [3]int, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, " ("); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nums, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nums, false
		}
		nums[i] = n
	}
	return nums, true
}
