package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// HTTPProxyConfig is the server's outbound proxy (http_proxy): requests to an
// allowed site go through ProxyURL, everything else keeps the proxy environment
// variables (HTTPS_PROXY / NO_PROXY). It covers what the server fetches itself —
// release checks and downloads, skill imports, notification webhooks, Web Push,
// delivery APIs; a more specific setting (server.update.proxy, a delivery proxy)
// still wins. File-only: it decides where the server's traffic goes.
type HTTPProxyConfig struct {
	// ProxyURL is an http, https or socks5 URL; "" = off.
	ProxyURL string `yaml:"proxy_url,omitempty"`
	// AllowSites are the hosts that use the proxy: "example.com" matches it and its
	// subdomains, "*.example.com" only the subdomains, "*" every host. Empty = every
	// host.
	AllowSites []string `yaml:"allow_sites,omitempty"`
}

// Enabled reports whether a proxy URL is configured.
func (p HTTPProxyConfig) Enabled() bool { return strings.TrimSpace(p.ProxyURL) != "" }

// Parsed is ProxyURL as a URL (nil when off or invalid; validate rejects invalid).
func (p HTTPProxyConfig) Parsed() *url.URL {
	if !p.Enabled() {
		return nil
	}
	u, err := parseProxyURL(p.ProxyURL)
	if err != nil {
		return nil
	}
	return u
}

// Matches reports whether host (with or without a port) uses the proxy.
func (p HTTPProxyConfig) Matches(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
	if host == "" {
		return false
	}
	if len(p.AllowSites) == 0 {
		return true
	}
	for _, site := range p.AllowSites {
		site = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(site), "."))
		switch {
		case site == "*":
			return true
		case strings.HasPrefix(site, "*."):
			if strings.HasSuffix(host, site[1:]) {
				return true
			}
		case site != "" && (host == site || strings.HasSuffix(host, "."+site)):
			return true
		}
	}
	return false
}

func parseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
		return nil, fmt.Errorf("must be an http, https or socks5 URL")
	}
	return u, nil
}

func (p HTTPProxyConfig) validate() error {
	if p.Enabled() {
		if _, err := parseProxyURL(p.ProxyURL); err != nil {
			return fmt.Errorf("http_proxy.proxy_url %q %v", p.ProxyURL, err)
		}
	} else if len(p.AllowSites) > 0 {
		return fmt.Errorf("http_proxy.allow_sites needs http_proxy.proxy_url")
	}
	for i, site := range p.AllowSites {
		s := strings.TrimSpace(site)
		rest := strings.TrimPrefix(s, "*.")
		if s == "" || (s != "*" && (rest == "" || strings.ContainsAny(rest, "/:*@ "))) {
			return fmt.Errorf("http_proxy.allow_sites[%d] %q must be a host name, *.domain or *", i, site)
		}
	}
	return nil
}
