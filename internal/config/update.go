package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Update check (gofer-6x2n, docs/design/2026-10-13-update-check-design.md): the
// server looks for a newer gofer release on a timer and on demand. It only CHECKS;
// upgrading stays `gofer serve upgrade` / `gofer worker upgrade`. FILE-ONLY: the
// server fetches these URLs and shows the answer, so an editable source would let a
// caller point the server at any internal address.

const (
	// DefaultUpdateInterval is the periodic check interval when interval is unset.
	DefaultUpdateInterval = 2 * time.Hour
	// MinUpdateInterval keeps the timer from hammering the source.
	MinUpdateInterval = 10 * time.Minute
	// DefaultUpdateRepo is the GitHub repository checked when no mirror is set.
	DefaultUpdateRepo = "inhere/gofer"
)

// UpdateConfig is server.update.
type UpdateConfig struct {
	// Enabled runs the periodic check (nil = on). A manual check works either way.
	Enabled *bool `yaml:"enabled,omitempty"`
	// Interval between periodic checks, a Go duration ("2h"); "" = DefaultUpdateInterval.
	Interval string `yaml:"interval,omitempty"`
	// Proxy for reaching the source (http / https / socks5 URL); "" = the proxy
	// environment variables.
	Proxy string `yaml:"proxy,omitempty"`
	// Repo is the GitHub owner/name whose latest release is checked ("" = DefaultUpdateRepo).
	Repo string `yaml:"repo,omitempty"`
	// LatestURL switches to a mirror: the URL of its latest.yaml ({name} placeholder).
	LatestURL string `yaml:"latest_url,omitempty"`
	// URLTemplate builds the mirror's download link ({name} {os} {arch} {ext} {version}).
	URLTemplate string `yaml:"url_template,omitempty"`
}

// IsEnabled reports whether the periodic check runs (nil/absent = on).
func (u UpdateConfig) IsEnabled() bool { return u.Enabled == nil || *u.Enabled }

// EffectiveInterval is the periodic check interval after defaulting.
func (u UpdateConfig) EffectiveInterval() time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(u.Interval))
	if err != nil || d <= 0 {
		return DefaultUpdateInterval
	}
	if d < MinUpdateInterval {
		return MinUpdateInterval
	}
	return d
}

// EffectiveRepo is the GitHub repository after defaulting.
func (u UpdateConfig) EffectiveRepo() string {
	if r := strings.TrimSpace(u.Repo); r != "" {
		return r
	}
	return DefaultUpdateRepo
}

// UsesMirror reports whether a mirror replaces GitHub.
func (u UpdateConfig) UsesMirror() bool { return strings.TrimSpace(u.LatestURL) != "" }

func (u UpdateConfig) validate() error {
	if s := strings.TrimSpace(u.Interval); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("server.update.interval %q: %v", s, err)
		}
		if d < MinUpdateInterval {
			return fmt.Errorf("server.update.interval %q is below the minimum %s", s, MinUpdateInterval)
		}
	}
	if r := strings.TrimSpace(u.Repo); r != "" && strings.Count(r, "/") != 1 {
		return fmt.Errorf("server.update.repo %q must be owner/name", r)
	}
	for _, f := range []struct{ name, val string }{{"latest_url", u.LatestURL}, {"url_template", u.URLTemplate}} {
		if v := strings.TrimSpace(f.val); v != "" {
			p, err := url.Parse(strings.NewReplacer("{name}", "x", "{os}", "x", "{arch}", "x", "{ext}", "", "{version}", "x").Replace(v))
			if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
				return fmt.Errorf("server.update.%s %q must be an http(s) URL", f.name, v)
			}
		}
	}
	if strings.TrimSpace(u.URLTemplate) != "" && !u.UsesMirror() {
		return fmt.Errorf("server.update.url_template needs latest_url (the mirror's latest.yaml)")
	}
	if p := strings.TrimSpace(u.Proxy); p != "" {
		pu, err := url.Parse(p)
		if err != nil || pu.Host == "" || (pu.Scheme != "http" && pu.Scheme != "https" && pu.Scheme != "socks5" && pu.Scheme != "socks5h") {
			return fmt.Errorf("server.update.proxy %q must be an http, https or socks5 URL", p)
		}
	}
	return nil
}
