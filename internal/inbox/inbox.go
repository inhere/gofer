// Package inbox projects Today cards into the two stable user-facing columns.
package inbox

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/today"
)

const (
	ColumnAction = "action"
	ColumnFYI    = "fyi"
)

type Item struct {
	Key            string         `json:"key"`
	Column         string         `json:"column"`
	Kind           string         `json:"kind"`
	Title          string         `json:"title"`
	Summary        string         `json:"summary"`
	SourceRevision string         `json:"source_revision"`
	State          string         `json:"state"`
	Actions        []today.Action `json:"actions"`
	Refs           today.Refs     `json:"refs"`
	DeepLink       string         `json:"deep_link,omitempty"`
	ProjectKey     string         `json:"project_key,omitempty"`
	ActivityAt     int64          `json:"activity_at"`
}

type State struct {
	Seen    bool
	Cleared bool
}

// Column applies first-match-wins rules. Empty rules mean the built-in policy.
func Column(card today.Card, cfg *config.InboxConfig) string {
	rules := config.DefaultInboxRules()
	if cfg != nil && len(cfg.Rules) > 0 {
		rules = append([]config.InboxRule(nil), cfg.Rules...)
	}
	sort.SliceStable(rules, func(i, j int) bool { return rules[i].Priority > rules[j].Priority })
	for _, r := range rules {
		if len(r.Kinds) > 0 && !contains(r.Kinds, card.Kind) {
			continue
		}
		if len(r.Projects) > 0 && !contains(r.Projects, card.ProjectKey) {
			continue
		}
		return r.Column
	}
	return ""
}

func NewItem(card today.Card, cfg *config.InboxConfig, state State) (Item, bool) {
	column := Column(card, cfg)
	if column == "" {
		return Item{}, false
	}
	b, _ := json.Marshal(struct {
		K        string
		Kind     string
		Title    string
		Summary  string
		Activity int64
		Actions  []today.Action
	}{card.Key, card.Kind, card.Title, card.Summary, card.ActivityAt, card.Actions})
	h := sha256.Sum256(b)
	st := "unread"
	if state.Cleared {
		st = "cleared"
	} else if state.Seen {
		st = "seen"
	}
	return Item{Key: card.Key, Column: column, Kind: card.Kind, Title: card.Title, Summary: card.Summary, SourceRevision: hex.EncodeToString(h[:]), State: st, Actions: card.Actions, Refs: card.Refs, ProjectKey: card.ProjectKey, ActivityAt: card.ActivityAt}, true
}

func contains(xs []string, v string) bool {
	for _, x := range xs {
		if strings.EqualFold(strings.TrimSpace(x), v) {
			return true
		}
	}
	return false
}
