package inbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/notify"
)

// Rule sources reported by the rule directory.
const (
	SourceDefault = "default"
	SourceConfig  = "config"
)

// KindInfo is one inbox kind in the rule directory: where the built-in policy puts it,
// where the effective rules put it now, and which notify events announce it.
type KindInfo struct {
	Kind          string `json:"kind"`
	DefaultColumn string `json:"default_column"`
	// Column is the effective column for a card of this kind with no project; "" means no
	// rule classifies it, so the item is not shown. Project-scoped rules are not applied
	// here (they depend on the card's project).
	Column string `json:"column"`
	// Source is "config" when server.inbox.rules decides, "default" for the built-in table.
	Source string   `json:"source"`
	RuleID string   `json:"rule_id,omitempty"`
	Events []string `json:"events"`
}

// EventInfo is a notify catalog event with the inbox kinds it announces. Kinds is empty for
// events that never reach the inbox. Default / DesktopDefault are the channels' own
// default subscriptions, unchanged by the inbox rules.
type EventInfo struct {
	Key            string   `json:"key"`
	Group          string   `json:"group"`
	Zh             string   `json:"zh"`
	En             string   `json:"en"`
	Kinds          []string `json:"kinds"`
	Default        bool     `json:"default"`
	DesktopDefault bool     `json:"desktop_default"`
}

// RuleDirectory is the GET /v1/inbox/rules body.
type RuleDirectory struct {
	Enabled bool `json:"enabled"`
	// Version changes whenever the effective rules change; clients drop caches on a change.
	Version string `json:"version"`
	// Source is "config" when server.inbox.rules is set, else "default".
	Source   string             `json:"source"`
	Rules    []config.InboxRule `json:"rules"`
	Defaults []config.InboxRule `json:"defaults"`
	Kinds    []KindInfo         `json:"kinds"`
	Events   []EventInfo        `json:"events"`
}

// EffectiveRules returns the rules in force: server.inbox.rules, else the built-in table.
func EffectiveRules(cfg *config.InboxConfig) ([]config.InboxRule, string) {
	if cfg != nil && len(cfg.Rules) > 0 {
		return slices.Clone(cfg.Rules), SourceConfig
	}
	return config.DefaultInboxRules(), SourceDefault
}

// RulesVersion is a short stable digest of the effective rules (not of enabled).
func RulesVersion(cfg *config.InboxConfig) string {
	rules, _ := EffectiveRules(cfg)
	b, _ := json.Marshal(rules)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:6])
}

// Directory builds the rule directory for the effective config. Only keys, columns and
// catalog descriptions are exposed: no webhook, path or secret.
func Directory(cfg *config.InboxConfig) RuleDirectory {
	rules, source := EffectiveRules(cfg)
	d := RuleDirectory{Enabled: cfg.IsEnabled(), Version: RulesVersion(cfg), Source: source, Rules: rules, Defaults: config.DefaultInboxRules()}
	for _, k := range config.InboxKinds() {
		r, custom := matchRule(k, "", cfg)
		info := KindInfo{Kind: k, DefaultColumn: config.InboxDefaultColumn(k), Column: r.Column, Source: SourceDefault, RuleID: r.ID, Events: config.InboxKindEvents(k)}
		if custom {
			info.Source = SourceConfig
		}
		d.Kinds = append(d.Kinds, info)
	}
	for _, e := range notify.EventCatalog() {
		d.Events = append(d.Events, EventInfo{Key: e.Key, Group: e.Group, Zh: e.Zh, En: e.En, Kinds: nonNil(config.InboxEventKinds(e.Key)), Default: e.Default, DesktopDefault: e.DesktopDefault})
	}
	return d
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}
