// Package netproxy routes the server's outbound HTTP through the configured
// http_proxy for the allowed sites (gofer-29gi). Install replaces the proxy
// function of http.DefaultTransport, so every client built on it — a nil
// Transport or a Clone — follows the current config without being rewired.
package netproxy

import (
	"net/http"
	"net/url"
	"sync"

	"github.com/inhere/gofer/internal/config"
)

var (
	mu  sync.RWMutex
	get func() config.HTTPProxyConfig
)

// Install makes http.DefaultTransport ask cfg (read per request, so a config
// reload applies at once) which proxy to use.
func Install(cfg func() config.HTTPProxyConfig) {
	mu.Lock()
	get = cfg
	mu.Unlock()
	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.Proxy = Proxy
	}
}

// Proxy is an http.Transport Proxy func: the configured proxy for an allowed host,
// else the environment's (HTTPS_PROXY / NO_PROXY).
func Proxy(req *http.Request) (*url.URL, error) {
	if u := For(req.URL); u != nil {
		return u, nil
	}
	return http.ProxyFromEnvironment(req)
}

// For is the configured proxy for target, nil when it does not apply.
func For(target *url.URL) *url.URL {
	mu.RLock()
	g := get
	mu.RUnlock()
	if g == nil || target == nil {
		return nil
	}
	cfg := g()
	if !cfg.Enabled() || !cfg.Matches(target.Host) {
		return nil
	}
	return cfg.Parsed()
}
