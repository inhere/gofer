package netproxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/inhere/gofer/internal/config"
)

func TestForMatchesAllowedSites(t *testing.T) {
	cfg := config.HTTPProxyConfig{ProxyURL: "http://127.0.0.1:10801", AllowSites: []string{"github.com", "*.githubusercontent.com"}}
	mu.Lock()
	old := get
	get = func() config.HTTPProxyConfig { return cfg }
	mu.Unlock()
	t.Cleanup(func() { mu.Lock(); get = old; mu.Unlock() })

	for _, tc := range []struct {
		target string
		proxy  bool
	}{
		{"https://github.com/o/r", true},
		{"https://api.github.com/repos", true},
		{"https://release-assets.githubusercontent.com/x", true},
		{"https://githubusercontent.com/x", false},
		{"https://mirror.example.com/tools", false},
		{"https://notgithub.com/", false},
	} {
		u, _ := url.Parse(tc.target)
		got := For(u)
		if (got != nil) != tc.proxy || (got != nil && got.Host != "127.0.0.1:10801") {
			t.Errorf("%s: proxy = %v, want %v", tc.target, got, tc.proxy)
		}
	}

	cfg = config.HTTPProxyConfig{ProxyURL: "socks5://127.0.0.1:1080"} // no allow_sites = every host
	if u, _ := url.Parse("https://anything.example/"); For(u) == nil || For(u).Scheme != "socks5" {
		t.Fatal("an empty allow_sites must proxy every host")
	}
	cfg = config.HTTPProxyConfig{} // off
	if u, _ := url.Parse("https://github.com/"); For(u) != nil {
		t.Fatal("no proxy_url must not proxy")
	}
}

// The default transport sends a matched request through the proxy, read per request.
func TestInstallRoutesDefaultTransport(t *testing.T) {
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("via proxy"))
	}))
	defer proxy.Close()
	cfg := config.HTTPProxyConfig{ProxyURL: proxy.URL, AllowSites: []string{"release.example"}}
	tr := http.DefaultTransport.(*http.Transport)
	oldProxy := tr.Proxy
	mu.RLock()
	oldGet := get
	mu.RUnlock()
	Install(func() config.HTTPProxyConfig { return cfg })
	t.Cleanup(func() {
		tr.Proxy = oldProxy
		mu.Lock()
		get = oldGet
		mu.Unlock()
	})

	client := &http.Client{Transport: tr.Clone()}
	resp, err := client.Get("http://release.example/latest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("proxy hits = %d, want 1", hits.Load())
	}
	cfg.AllowSites = []string{"other.example"} // a reload: the same client stops using it
	if _, err := client.Get("http://release.example/latest.yaml"); err == nil && hits.Load() != 1 {
		t.Fatalf("a site outside allow_sites went through the proxy")
	}
}
