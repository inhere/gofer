package config

import (
	"strings"
	"testing"
)

func TestValidateCallerNames(t *testing.T) {
	t.Parallel()
	cfgOf := func(cs ...CallerConfig) *Config {
		return &Config{Server: ServerConfig{Callers: cs}}
	}
	t.Run("trimmed and optional", func(t *testing.T) {
		t.Parallel()
		cfg := cfgOf(CallerConfig{ID: "default", Name: "  admin  "}, CallerConfig{ID: "bob"})
		if err := validate(cfg); err != nil {
			t.Fatalf("validate() = %v", err)
		}
		if got := cfg.Server.Callers[0].Name; got != "admin" {
			t.Errorf("name = %q, want trimmed", got)
		}
		names := cfg.Server.CallerNames()
		if len(names) != 1 || names["default"] != "admin" {
			t.Errorf("CallerNames() = %v, want only default->admin", names)
		}
		if cfg.Server.CallerName("bob") != "" {
			t.Errorf("a caller without a name must report empty")
		}
	})
	cases := map[string]struct {
		callers []CallerConfig
		want    string
	}{
		"duplicate": {[]CallerConfig{{ID: "a", Name: "Admin"}, {ID: "b", Name: "admin"}}, "unique"},
		"too long":  {[]CallerConfig{{ID: "a", Name: strings.Repeat("x", MaxCallerNameLen+1)}}, "at most"},
		"control":   {[]CallerConfig{{ID: "a", Name: "ad\nmin"}}, "control"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validate(cfgOf(tc.callers...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
