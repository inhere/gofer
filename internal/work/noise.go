package work

// Noise reduction (docs/design/2026-10-20-work-noise-and-db-settings-design.md §总体方案 1):
// the draft gate, the exclusion lists and the short-session rule. The thresholds and lists
// are extension settings (internal/settings, applied through SetSettings); the rules do not
// touch items that already exist.

import (
	"log/slog"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/settings"
)

// Settings is the slice of the extension-settings service (internal/settings) the noise
// rules read. An interface keeps this package off the entry layers and lets a test supply
// its own values (G022).
type Settings interface {
	GetInt(key string) (int, error)
	GetString(key string) (string, error)
	GetStrings(key string) ([]string, error)
	Patterns(key string) ([]*regexp.Regexp, error)
}

// SetSettings injects the extension settings. nil (or a nil service) means "registry
// defaults", so a service built without one behaves like a fresh install.
func (s *Service) SetSettings(st Settings) { s.settings = st }

// settingInt reads an int setting, falling back to its registry default — the summarizer
// and the work loop must not stop because the settings table could not be read.
func (s *Service) settingInt(key string) int {
	if s.settings != nil {
		v, err := s.settings.GetInt(key)
		if err == nil {
			return v
		}
		slog.Warn("work.settings_read_failed", "event", "work.settings_read_failed", "key", key, "err", err)
	}
	if d, ok := settings.Lookup(key); ok {
		if v, ok := d.Default.(int); ok {
			return v
		}
	}
	return 0
}

// settingString reads a text setting (a prompt), falling back to its registry default on
// any read failure: a prompt is sent with every run, so it must never block one.
func (s *Service) settingString(key string) string {
	if s.settings != nil {
		v, err := s.settings.GetString(key)
		if err == nil {
			return v
		}
		slog.Warn("work.settings_read_failed", "event", "work.settings_read_failed", "key", key, "err", err)
	}
	if d, ok := settings.Lookup(key); ok {
		if v, ok := d.Default.(string); ok {
			return v
		}
	}
	return ""
}

// settingStrings reads a string-list setting (default on any read failure).
func (s *Service) settingStrings(key string) []string {
	if s.settings != nil {
		v, err := s.settings.GetStrings(key)
		if err == nil {
			return v
		}
		slog.Warn("work.settings_read_failed", "event", "work.settings_read_failed", "key", key, "err", err)
	}
	if d, ok := settings.Lookup(key); ok {
		if v, ok := d.Default.([]string); ok {
			return v
		}
	}
	return nil
}

// settingPatterns reads a regex-list setting; a value that does not compile (only a
// registry default can be wrong here — the service validates on write) is dropped with a
// warning rather than silencing the rule.
func (s *Service) settingPatterns(key string) []*regexp.Regexp {
	if s.settings != nil {
		v, err := s.settings.Patterns(key)
		if err == nil {
			return v
		}
		slog.Warn("work.settings_read_failed", "event", "work.settings_read_failed", "key", key, "err", err)
	}
	var out []*regexp.Regexp
	for _, p := range s.settingStrings(key) {
		re, err := regexp.Compile(p)
		if err != nil {
			slog.Warn("work.settings_bad_pattern", "event", "work.settings_bad_pattern", "key", key, "pattern", p, "err", err)
			continue
		}
		out = append(out, re)
	}
	return out
}

// draftExclusion says why the session must not get a draft, "" when it may (design
// §关键流程: 无项目不建；项目 / 目录 / 标题命中的不建).
func (s *Service) draftExclusion(a jobstore.AgentSession, title string) string {
	if strings.TrimSpace(a.ProjectKey) == "" {
		return "会话不属于任何项目"
	}
	for _, p := range s.settingStrings(settings.KeyWorkDraftExcludeProjects) {
		if p = strings.TrimSpace(p); p != "" && strings.EqualFold(p, strings.TrimSpace(a.ProjectKey)) {
			return "项目在排除名单里：" + p
		}
	}
	for _, p := range s.settingStrings(settings.KeyWorkDraftExcludeCwds) {
		if pathHasPrefix(a.Cwd, p) {
			return "目录在排除名单里：" + strings.TrimSpace(p)
		}
	}
	for _, re := range s.settingPatterns(settings.KeyWorkDraftExcludeTitlePatterns) {
		if re.MatchString(title) {
			return "标题命中排除正则：" + re.String()
		}
	}
	return ""
}

// pathHasPrefix reports whether cwd is prefix itself or below it. Separators and case are
// normalised for the platform: Windows compares case-insensitively and both spellings of
// the separator mean the same directory.
func pathHasPrefix(cwd, prefix string) bool {
	cwd, prefix = strings.TrimSpace(cwd), strings.TrimSpace(prefix)
	if cwd == "" || prefix == "" {
		return false
	}
	cwd = filepath.ToSlash(filepath.Clean(cwd))
	prefix = filepath.ToSlash(filepath.Clean(prefix))
	if runtime.GOOS == "windows" {
		cwd, prefix = strings.ToLower(cwd), strings.ToLower(prefix)
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if cwd == prefix {
		return true
	}
	return strings.HasPrefix(cwd, prefix+"/")
}

// ShortSession reports whether the session is a short-lived one the summarizer and the
// steward skip, with the reason (design §关键流程). The thresholds are the extension
// settings; a settings read failure falls back to the registry defaults.
func (s *Service) ShortSession(a jobstore.AgentSession, now time.Time) (string, bool) {
	return jobstore.ShortSession(a, s.settingInt(settings.KeyWorkShortSessionMaxMinutes),
		s.settingInt(settings.KeyWorkShortSessionMaxPrompts), now.Unix())
}

// promptText composes one prompt from its two setting keys: the replacement (else def),
// then the operator's extra block (settings.ComposePrompt).
func (s *Service) promptText(replaceKey, extraKey, def string) string {
	return settings.ComposePrompt(settings.ChoosePrompt(def, s.settingString(replaceKey)), s.settingString(extraKey))
}

// DropShortItems removes the items whose current sessions are ALL short-lived: the steward
// neither reviews nor primes those. An item with at least one session that is not short
// stays, and so does an item without a current session (nothing to judge — a hand-made
// item, a job-only item). A store error keeps every item: a read failure must never
// silence the steward.
func (s *Service) DropShortItems(items []ItemView, now time.Time) []ItemView {
	if len(items) == 0 {
		return items
	}
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	cur, err := s.store.CurrentWorkSessionIDs(ids)
	if err != nil {
		return items
	}
	out := make([]ItemView, 0, len(items))
	for _, it := range items {
		if s.itemIsShort(cur[it.ID], now) {
			continue
		}
		out = append(out, it)
	}
	return out
}

// itemIsShort reports whether every current session of an item is short-lived (and there
// is at least one).
func (s *Service) itemIsShort(sids []string, now time.Time) bool {
	if len(sids) == 0 {
		return false
	}
	maxMinutes := s.settingInt(settings.KeyWorkShortSessionMaxMinutes)
	maxPrompts := s.settingInt(settings.KeyWorkShortSessionMaxPrompts)
	for _, sid := range sids {
		a, ok, err := s.store.GetAgentSession(sid)
		if err != nil || !ok {
			return false
		}
		if _, short := jobstore.ShortSession(a, maxMinutes, maxPrompts, now.Unix()); !short {
			return false
		}
	}
	return true
}
