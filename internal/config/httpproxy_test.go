package config

import (
	"strings"
	"testing"
)

func TestHTTPProxyValidate(t *testing.T) {
	ok := []HTTPProxyConfig{
		{},
		{ProxyURL: "http://127.0.0.1:10801"},
		{ProxyURL: "socks5://127.0.0.1:1080", AllowSites: []string{"github.com", "*.githubusercontent.com", "*"}},
	}
	for _, p := range ok {
		if err := p.validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	for _, tc := range []struct {
		p    HTTPProxyConfig
		want string
	}{
		{HTTPProxyConfig{ProxyURL: "127.0.0.1:10801"}, "proxy_url"},
		{HTTPProxyConfig{ProxyURL: "ftp://h:1"}, "proxy_url"},
		{HTTPProxyConfig{AllowSites: []string{"github.com"}}, "needs http_proxy.proxy_url"},
		{HTTPProxyConfig{ProxyURL: "http://h:1", AllowSites: []string{"https://github.com"}}, "allow_sites[0]"},
		{HTTPProxyConfig{ProxyURL: "http://h:1", AllowSites: []string{"github.com/x"}}, "allow_sites[0]"},
		{HTTPProxyConfig{ProxyURL: "http://h:1", AllowSites: []string{"a.com", ""}}, "allow_sites[1]"},
		{HTTPProxyConfig{ProxyURL: "http://h:1", AllowSites: []string{"*."}}, "allow_sites[0]"},
	} {
		if err := tc.p.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: err = %v, want %q", tc.p, err, tc.want)
		}
	}
}

func TestHTTPProxyMatches(t *testing.T) {
	p := HTTPProxyConfig{ProxyURL: "http://h:1", AllowSites: []string{"GitHub.com", "*.githubusercontent.com"}}
	for host, want := range map[string]bool{
		"github.com":                           true,
		"api.github.com:443":                   true,
		"release-assets.githubusercontent.com": true,
		"githubusercontent.com":                false,
		"evilgithub.com":                       false,
		"":                                     false,
	} {
		if got := p.Matches(host); got != want {
			t.Errorf("Matches(%q) = %v, want %v", host, got, want)
		}
	}
}
