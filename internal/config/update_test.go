package config

import (
	"strings"
	"testing"
	"time"
)

func TestUpdateConfigDefaultsAndValidation(t *testing.T) {
	var u UpdateConfig
	if !u.IsEnabled() || u.EffectiveInterval() != DefaultUpdateInterval || u.EffectiveRepo() != DefaultUpdateRepo || u.UsesMirror() {
		t.Fatalf("defaults: %+v", u)
	}
	ok := UpdateConfig{Interval: "30m", LatestURL: "http://mirror.example.com/tools/{name}/latest.yaml",
		URLTemplate: "http://mirror.example.com/tools/{name}/{name}-{os}-{arch}{ext}", Proxy: "http://127.0.0.1:8080"}
	if err := ok.validate(); err != nil || ok.EffectiveInterval() != 30*time.Minute || !ok.UsesMirror() {
		t.Fatalf("valid config: %v", err)
	}
	for _, tc := range []struct {
		u    UpdateConfig
		want string
	}{
		{UpdateConfig{Interval: "soon"}, "interval"},
		{UpdateConfig{Interval: "1m"}, "below the minimum"},
		{UpdateConfig{Repo: "gofer"}, "owner/name"},
		{UpdateConfig{LatestURL: "ftp://x/latest.yaml"}, "http(s) URL"},
		{UpdateConfig{URLTemplate: "http://x/{name}"}, "needs latest_url"},
		{UpdateConfig{Proxy: "127.0.0.1:8080"}, "proxy"},
	} {
		if err := tc.u.validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.u, err, tc.want)
		}
	}
}
