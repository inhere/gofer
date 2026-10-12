package config

import (
	"fmt"
	"net/url"
	"strings"
)

// DeliveryConfig is a project's delivery of worktree jobs (GIT-02, design
// 2026-10-12-delivery-and-cost-estimate-design.md).
type DeliveryConfig struct {
	// Mode: none (default) | merge | push | pr. A job may override it (--deliver).
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
	// AllowPush permits pushing (push / pr, and merge's push_base). Only here — a
	// job cannot grant it to itself.
	AllowPush bool `yaml:"allow_push,omitempty" json:"allow_push,omitempty"`
	// Remote to push to; "" = origin.
	Remote string `yaml:"remote,omitempty" json:"remote,omitempty"`
	// Branch is the pushed branch name template: {{job_id}} {{short_id}} {{slug}};
	// "" = gofer/{{job_id}}.
	Branch string `yaml:"branch,omitempty" json:"branch,omitempty"`
	// Base is the merge / PR target branch; "" = the main checkout's branch.
	Base string `yaml:"base,omitempty" json:"base,omitempty"`
	// Draft opens the request as a draft (default true).
	Draft *bool `yaml:"draft,omitempty" json:"draft,omitempty"`
	// Squash merges as one commit (merge mode).
	Squash bool `yaml:"squash,omitempty" json:"squash,omitempty"`
	// PushBase pushes the base branch after a merge (needs AllowPush).
	PushBase bool `yaml:"push_base,omitempty" json:"push_base,omitempty"`
	// Cleanup removes the worktree after a successful delivery.
	Cleanup bool `yaml:"cleanup,omitempty" json:"cleanup,omitempty"`
	// Labels for the request (where the platform supports them).
	Labels []string `yaml:"labels,omitempty" json:"labels,omitempty"`
	// Provider: auto (default) | github | gitlab | gitea (Forgejo too).
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// TokenEnv names the environment variable holding the platform token on the
	// machine that delivers; "" = the provider's defaults.
	TokenEnv string `yaml:"token_env,omitempty" json:"token_env,omitempty"`
	// Proxy (http://, https://, socks5://) carries the push and the platform API calls
	// of a delivery, for a machine that reaches the platform only through a proxy.
	// "" = git's own http.proxy for the remote, then the proxy environment variables.
	Proxy string `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	// JobLink adds the job's web console link (server.web_base_url) to the request
	// body. Off by default: the console is usually on a private address, and the
	// request may be public.
	JobLink bool `yaml:"job_link,omitempty" json:"job_link,omitempty"`
}

// DeliveryModes lists the accepted modes.
var DeliveryModes = []string{"none", "merge", "push", "pr"}

// ValidDeliveryMode reports whether m is a mode ("" = unset).
func ValidDeliveryMode(m string) bool {
	if m == "" {
		return true
	}
	for _, x := range DeliveryModes {
		if m == x {
			return true
		}
	}
	return false
}

// IsDraft is Draft with its default (true).
func (d DeliveryConfig) IsDraft() bool { return d.Draft == nil || *d.Draft }

// validate checks one project's delivery block.
func (d *DeliveryConfig) validate(project string) error {
	if d == nil {
		return nil
	}
	where := "projects." + project + ".delivery"
	if !ValidDeliveryMode(d.Mode) {
		return fmt.Errorf("%s.mode: %q is not one of %s", where, d.Mode, strings.Join(DeliveryModes, ", "))
	}
	switch strings.ToLower(d.Provider) {
	case "", "auto", "github", "gitlab", "gitea", "forgejo":
	default:
		return fmt.Errorf("%s.provider: %q is not auto, github, gitlab or gitea", where, d.Provider)
	}
	if d.TokenEnv != "" && !envNamePattern.MatchString(d.TokenEnv) {
		return fmt.Errorf("%s.token_env: %q must be an environment variable name", where, d.TokenEnv)
	}
	if d.Proxy != "" {
		u, err := url.Parse(d.Proxy)
		if err != nil || u.Host == "" {
			return fmt.Errorf("%s.proxy: %q is not a proxy URL (http://host:port)", where, d.Proxy)
		}
		switch u.Scheme {
		case "http", "https", "socks5", "socks5h":
		default:
			return fmt.Errorf("%s.proxy: scheme %q is not http, https or socks5", where, u.Scheme)
		}
	}
	if strings.ContainsAny(d.Branch, " ~^:?*[\\") {
		return fmt.Errorf("%s.branch: %q is not a valid branch name template", where, d.Branch)
	}
	return nil
}

// ValidateDelivery checks every project's delivery block.
func (c *Config) ValidateDelivery() error {
	for key, p := range c.Projects {
		if err := p.Delivery.validate(key); err != nil {
			return err
		}
	}
	return nil
}
