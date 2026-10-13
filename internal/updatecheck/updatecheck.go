// Package updatecheck looks for a newer gofer release (gofer-6x2n,
// docs/design/2026-10-13-update-check-design.md): the latest GitHub release by
// default, or a mirror's latest.yaml. It only reports; it never downloads or
// installs anything.
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
	if u.UsesMirror() {
		err = c.checkMirror(ctx, u, &st)
	} else {
		err = c.checkGitHub(ctx, u, &st)
	}
	if err != nil {
		st.Error = err.Error()
	} else {
		st.Newer = Newer(st.Latest, c.current)
	}
	c.mu.Lock()
	c.status = st
	announce := st.Newer && st.Latest != c.notified
	if announce {
		c.notified = st.Latest
	}
	c.mu.Unlock()
	if announce {
		slog.Info("update.available", "event", "update.available", "current", c.current, "latest", st.Latest, "source", st.Source)
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

func (c *Checker) checkGitHub(ctx context.Context, u config.UpdateConfig, st *Status) error {
	body, err := c.get(ctx, u, githubAPI+"/repos/"+u.EffectiveRepo()+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return err
	}
	var rel struct {
		TagName     string `json:"tag_name"`
		HTMLURL     string `json:"html_url"`
		PublishedAt string `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return fmt.Errorf("read the GitHub release: %v", err)
	}
	if rel.TagName == "" {
		return errors.New("the GitHub release has no tag")
	}
	st.Latest = strings.TrimPrefix(rel.TagName, "v")
	st.ReleaseURL, st.ReleasedAt = rel.HTMLURL, rel.PublishedAt
	prefix := AppName + "-" + c.goos + "-" + c.goarch
	for _, a := range rel.Assets {
		if strings.HasPrefix(a.Name, prefix) {
			st.DownloadURL = a.URL
			break
		}
	}
	return nil
}

func (c *Checker) checkMirror(ctx context.Context, u config.UpdateConfig, st *Status) error {
	body, err := c.get(ctx, u, c.expand(u.LatestURL, ""), "")
	if err != nil {
		return err
	}
	var meta struct {
		Name        string `yaml:"name"`
		Version     string `yaml:"version"`
		ReleasedAt  string `yaml:"released_at"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(body, &meta); err != nil {
		return fmt.Errorf("read latest.yaml: %v", err)
	}
	if strings.TrimSpace(meta.Version) == "" {
		return errors.New("latest.yaml has no version")
	}
	st.Latest = strings.TrimPrefix(strings.TrimSpace(meta.Version), "v")
	st.ReleasedAt, st.Description = meta.ReleasedAt, meta.Description
	if t := strings.TrimSpace(u.URLTemplate); t != "" {
		st.DownloadURL = c.expand(t, st.Latest)
	}
	return nil
}

// expand fills the {name} {os} {arch} {ext} {version} placeholders.
func (c *Checker) expand(tmpl, version string) string {
	ext := ""
	if c.goos == "windows" {
		ext = ".exe"
	}
	return strings.NewReplacer("{name}", AppName, "{os}", c.goos, "{arch}", c.goarch, "{ext}", ext, "{version}", version).Replace(strings.TrimSpace(tmpl))
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
