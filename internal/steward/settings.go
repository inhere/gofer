package steward

import (
	"log/slog"

	"github.com/inhere/gofer/internal/settings"
)

// The steward's prompts (design 2026-10-20 §「提示词的覆盖方式」, plan K4): the role
// (prompt.steward.role) and the review instruction (prompt.steward.review) can each be
// replaced, and each has a `.extra` key appended after whichever instruction is in effect.

// Settings is the slice of the extension-settings service the prompts read. An interface
// keeps this package off the entry layers and lets a test supply its own values (G022).
type Settings interface {
	GetString(key string) (string, error)
}

// SetSettings injects the extension settings. nil (or a nil service) means "registry
// defaults", so a steward built without one sends exactly the code prompts.
func (s *Service) SetSettings(st Settings) { s.settings = st }

// settingString reads a prompt setting, falling back to its registry default: a review must
// not fail because the settings table could not be read.
func (s *Service) settingString(key string) string {
	if s.settings != nil {
		v, err := s.settings.GetString(key)
		if err == nil {
			return v
		}
		slog.Warn("steward.settings_read_failed", "event", "steward.settings_read_failed", "key", key, "err", err)
	}
	if d, ok := settings.Lookup(key); ok {
		if v, ok := d.Default.(string); ok {
			return v
		}
	}
	return ""
}

// promptText composes one prompt from its two setting keys: the replacement (else def), then
// the operator's extra block (settings.ComposePrompt).
func (s *Service) promptText(replaceKey, extraKey, def string) string {
	return settings.ComposePrompt(settings.ChoosePrompt(def, s.settingString(replaceKey)), s.settingString(extraKey))
}

// roleText is the standing instruction of the steward session.
func (s *Service) roleText() string {
	return s.promptText(settings.KeyPromptStewardRole, settings.KeyPromptStewardRoleExtra, rolePrompt)
}
