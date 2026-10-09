package tracker

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Memory kinds (design 2026-10-09-prime-memory-quality-design.md §2.1).
const (
	MemoryKindRule    = "rule"
	MemoryKindNote    = "note"
	MemoryKindHandoff = "handoff"
)

// DefaultHandoffTTL is the lifetime of a handoff memory without an explicit --ttl.
const DefaultHandoffTTL = 14 * 24 * time.Hour

// MemorySummaryRequiredRunes: rule / note content longer than this needs a summary.
const MemorySummaryRequiredRunes = 200

// MemorySummaryMaxRunes caps summaries (written and derived) shown in indexes.
const MemorySummaryMaxRunes = 80

// staleNoteAge marks notes that have not been touched for a long time (§2.1).
const staleNoteAge = 90 * 24 * time.Hour

// MemoryWhen lists the scenario triggers of a memory (§2.9). P1 stores them and
// uses Paths for the cwd ordering in prime; the hook injection (P1b) matches
// Keywords against prompts and Commands (P5) against tool calls.
type MemoryWhen struct {
	Keywords []string `json:"keywords,omitempty"`
	Paths    []string `json:"paths,omitempty"`
	Commands []string `json:"commands,omitempty"`
}

func (w *MemoryWhen) empty() bool {
	return w == nil || (len(w.Keywords) == 0 && len(w.Paths) == 0 && len(w.Commands) == 0)
}

// MemoryMeta is the shared optional part of local, synced and scoped (server
// global / project) memories. It is embedded so the JSON stays flat; every field
// is omitempty, so old bodies round-trip unchanged.
type MemoryMeta struct {
	Kind      string      `json:"kind,omitempty"`
	Summary   string      `json:"summary,omitempty"`
	When      *MemoryWhen `json:"when,omitempty"`
	ExpiresAt string      `json:"expires_at,omitempty"`
	// Source is a free-form origin reference: issue:<id>, plan:<id>, job:<id>, session:<id>.
	Source    string `json:"source,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	// DoctorIgnore lists `memory doctor` finding slugs silenced for this memory
	// (known false positives, e.g. a rule that names a removed path on purpose).
	DoctorIgnore []string `json:"doctor_ignore,omitempty"`
}

// ValidMemoryKind reports whether kind is one of rule|note|handoff.
func ValidMemoryKind(kind string) bool {
	switch kind {
	case MemoryKindRule, MemoryKindNote, MemoryKindHandoff:
		return true
	}
	return false
}

// EffectiveMemoryKind is the kind used for display: the stored kind, else the
// legacy `prime` tag as rule, else note.
func EffectiveMemoryKind(meta MemoryMeta, tags []string) string {
	if meta.Kind != "" {
		return meta.Kind
	}
	// DEPRECATED(v0.129): remove in v0.132. The `prime` tag predates kind=rule;
	// it is mapped at read time until stores have been rewritten with --kind rule.
	if hasTag(tags, "prime") {
		return MemoryKindRule
	}
	return MemoryKindNote
}

// EffectiveKind returns the effective kind of the memory.
func (m Memory) EffectiveKind() string { return EffectiveMemoryKind(m.MemoryMeta, m.Tags) }

// MemoryCreatedAt falls back to updated_at for memories written before created_at existed.
func MemoryCreatedAt(meta MemoryMeta, updatedAt string) string {
	if meta.CreatedAt != "" {
		return meta.CreatedAt
	}
	return updatedAt
}

// MemoryExpiresAt is the handoff expiry: the stored expires_at, else updated_at +
// DefaultHandoffTTL. Non-handoff memories never expire (zero time, false).
func MemoryExpiresAt(meta MemoryMeta, tags []string, updatedAt string) (time.Time, bool) {
	if EffectiveMemoryKind(meta, tags) != MemoryKindHandoff {
		return time.Time{}, false
	}
	if t, ok := parseTime(meta.ExpiresAt); ok {
		return t, true
	}
	if t, ok := parseTime(updatedAt); ok {
		return t.Add(DefaultHandoffTTL), true
	}
	return time.Time{}, false
}

// MemoryExpired reports whether a handoff memory is past its expiry at now.
func MemoryExpired(meta MemoryMeta, tags []string, updatedAt string, now time.Time) bool {
	at, ok := MemoryExpiresAt(meta, tags, updatedAt)
	return ok && !now.Before(at)
}

// MemoryStale reports a note not updated for 90 days (§2.1 「久未更新」).
func MemoryStale(meta MemoryMeta, tags []string, updatedAt string, now time.Time) bool {
	if EffectiveMemoryKind(meta, tags) != MemoryKindNote {
		return false
	}
	t, ok := parseTime(updatedAt)
	return ok && now.Sub(t) >= staleNoteAge
}

func parseTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// AgeText renders the age of a timestamp as 今天 / N 天前 ("" when unparsable).
func AgeText(value string, now time.Time) string {
	t, ok := parseTime(value)
	if !ok {
		return ""
	}
	days := ageDays(t, now)
	if days <= 0 {
		return "今天"
	}
	return fmt.Sprintf("%d 天前", days)
}

func ageDays(t, now time.Time) int {
	if now.Before(t) {
		return 0
	}
	return int(now.Sub(t).Hours() / 24)
}

var (
	linkOnlyLine = regexp.MustCompile(`^(?:[-*+]\s+)?(?:<?https?://\S+>?|!?\[[^\]]*\]\([^)]*\))$`)
	listMarker   = regexp.MustCompile(`^(?:[-*+]|\d+[.)])\s+`)
)

// FallbackMemorySummary derives a summary for a memory without one: the first
// sentence of the first line that is not empty, not a heading (`#`, `【…】`) and
// not a bare link, capped at MemorySummaryMaxRunes.
func FallbackMemorySummary(content string) string {
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "【") || linkOnlyLine.MatchString(line) {
			continue
		}
		line = strings.TrimSpace(listMarker.ReplaceAllString(line, ""))
		if line == "" {
			continue
		}
		return truncateRunes(firstSentence(line), MemorySummaryMaxRunes)
	}
	return ""
}

func firstSentence(line string) string {
	runes := []rune(line)
	for i, r := range runes {
		switch r {
		case '。', '！', '？', '；':
			return string(runes[:i+1])
		case '.', '!', '?':
			if i == len(runes)-1 || runes[i+1] == ' ' {
				return string(runes[:i+1])
			}
		}
	}
	return line
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

// DisplayMemorySummary is the stored summary, else the derived one.
func DisplayMemorySummary(meta MemoryMeta, content string) string {
	if s := strings.TrimSpace(meta.Summary); s != "" {
		return truncateRunes(s, MemorySummaryMaxRunes)
	}
	return FallbackMemorySummary(content)
}

// MemoryPatch is one `memory set` write. Nil fields keep the stored value; a
// non-nil empty string / slice clears it. Content is always replaced.
type MemoryPatch struct {
	Content      string
	Tags         []string // nil keeps the stored tags
	Kind         *string
	Summary      *string
	Source       *string
	WhenKeywords *[]string
	WhenPaths    *[]string
	WhenCommands *[]string
	// TTL sets expires_at = now + TTL (handoff only); ExpiresAt sets it directly.
	TTL       *time.Duration
	ExpiresAt *string
	// DoctorIgnore replaces the silenced doctor slugs (nil keeps them).
	DoctorIgnore *[]string
	// DefaultSource fills source when neither the patch nor the stored memory has
	// one (the CLI passes job:<id> / session:<id> from its environment).
	DefaultSource string
	By            string
}

// ApplyMemoryPatch merges patch into existing (nil for a new memory) at now.
// It checks the kind and TTL; the summary requirement is ValidateMemoryForWrite.
func ApplyMemoryPatch(existing *Memory, key string, patch MemoryPatch, now time.Time) (Memory, error) {
	stamp := now.UTC().Format(time.RFC3339Nano)
	var item Memory
	if existing != nil {
		item = *existing
		item.Tags = append([]string(nil), existing.Tags...)
		item.DoctorIgnore = append([]string(nil), existing.DoctorIgnore...)
		if existing.When != nil {
			when := *existing.When
			item.When = &when
		}
	}
	item.Key, item.Content, item.UpdatedAt, item.By = key, patch.Content, stamp, patch.By
	if patch.Tags != nil {
		item.Tags = addTags(nil, patch.Tags)
	}
	if item.CreatedAt == "" {
		if existing != nil && existing.UpdatedAt != "" {
			item.CreatedAt = existing.UpdatedAt
		} else {
			item.CreatedAt = stamp
		}
	}
	prevKind := ""
	if existing != nil {
		prevKind = existing.EffectiveKind()
	}
	if patch.Kind != nil {
		kind := strings.TrimSpace(*patch.Kind)
		if kind != "" && !ValidMemoryKind(kind) {
			return Memory{}, fmt.Errorf("invalid memory kind %q (rule|note|handoff)", kind)
		}
		item.Kind = kind
	}
	if patch.Summary != nil {
		item.Summary = strings.TrimSpace(*patch.Summary)
	}
	if patch.Source != nil {
		item.Source = strings.TrimSpace(*patch.Source)
	} else if item.Source == "" {
		item.Source = strings.TrimSpace(patch.DefaultSource)
	}
	if patch.DoctorIgnore != nil {
		item.DoctorIgnore = addTags(nil, *patch.DoctorIgnore)
	}
	if patch.WhenKeywords != nil || patch.WhenPaths != nil || patch.WhenCommands != nil {
		when := MemoryWhen{}
		if item.When != nil {
			when = *item.When
		}
		if patch.WhenKeywords != nil {
			when.Keywords = addTags(nil, *patch.WhenKeywords)
		}
		if patch.WhenPaths != nil {
			when.Paths = addTags(nil, *patch.WhenPaths)
		}
		if patch.WhenCommands != nil {
			when.Commands = addTags(nil, *patch.WhenCommands)
		}
		item.When = &when
		if when.empty() {
			item.When = nil
		}
	}
	kind := item.EffectiveKind()
	if patch.TTL != nil || patch.ExpiresAt != nil {
		if kind != MemoryKindHandoff {
			return Memory{}, errors.New("--ttl only applies to --kind handoff")
		}
	}
	switch {
	case patch.TTL != nil:
		if *patch.TTL <= 0 {
			return Memory{}, errors.New("ttl must be positive")
		}
		item.ExpiresAt = now.UTC().Add(*patch.TTL).Format(time.RFC3339)
	case patch.ExpiresAt != nil:
		item.ExpiresAt = strings.TrimSpace(*patch.ExpiresAt)
	case kind == MemoryKindHandoff && (prevKind != MemoryKindHandoff || item.ExpiresAt == ""):
		item.ExpiresAt = now.UTC().Add(DefaultHandoffTTL).Format(time.RFC3339)
	case kind != MemoryKindHandoff:
		item.ExpiresAt = ""
	}
	return item, nil
}

// ValidateMemoryForWrite applies the CLI write rule (§2.2): rule / note content
// longer than MemorySummaryRequiredRunes needs a summary. The error proposes the
// derived first sentence as a candidate.
func ValidateMemoryForWrite(item Memory) error {
	if strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.Content) == "" {
		return errors.New("memory key and content are required")
	}
	if n := len([]rune(item.Summary)); n > MemorySummaryMaxRunes {
		return fmt.Errorf("summary is %d chars, keep it within %d", n, MemorySummaryMaxRunes)
	}
	if item.EffectiveKind() == MemoryKindHandoff || strings.TrimSpace(item.Summary) != "" {
		return nil
	}
	if n := len([]rune(item.Content)); n > MemorySummaryRequiredRunes {
		msg := fmt.Sprintf("memory content is %d chars (> %d): --summary \"<一句话：这条讲什么、什么时候该看>\" is required", n, MemorySummaryRequiredRunes)
		if candidate := FallbackMemorySummary(item.Content); candidate != "" {
			msg += fmt.Sprintf("; candidate: --summary %s", strconv.Quote(candidate))
		}
		return errors.New(msg)
	}
	return nil
}

// ParseMemoryTTL accepts Nd / Nw plus Go durations (36h, 90m).
func ParseMemoryTTL(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("empty ttl")
	}
	unit := value[len(value)-1]
	if unit == 'd' || unit == 'w' {
		n, err := strconv.Atoi(value[:len(value)-1])
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid ttl %q (e.g. 14d, 2w, 36h)", value)
		}
		day := 24 * time.Hour
		if unit == 'w' {
			day *= 7
		}
		return time.Duration(n) * day, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid ttl %q (e.g. 14d, 2w, 36h)", value)
	}
	return d, nil
}

// MatchMemoryPath reports whether the slash-separated repository-relative path
// rel (a cwd or a touched file) falls under a `when.paths` glob. `**` matches any
// number of segments, `*` / `?` stay inside one segment, and a pattern without
// glob characters matches the directory itself and everything below it. The
// repository root ("" / ".") matches nothing but `**`.
func MatchMemoryPath(pattern, rel string) bool {
	pattern = strings.Trim(strings.TrimSpace(strings.ReplaceAll(pattern, "\\", "/")), "/")
	rel = strings.Trim(path.Clean("/"+strings.ReplaceAll(rel, "\\", "/")), "/")
	if pattern == "" {
		return false
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return rel == pattern || strings.HasPrefix(rel, pattern+"/")
	}
	re, err := globRegexp(pattern)
	if err != nil {
		return false
	}
	// `web/**` should also hold for the directory `web` itself.
	return re.MatchString(rel) || (rel != "" && re.MatchString(rel+"/"))
}

func globRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// MemoryMatchesPath reports whether any when.paths glob matches rel.
func MemoryMatchesPath(meta MemoryMeta, rel string) bool {
	if meta.When == nil {
		return false
	}
	for _, pattern := range meta.When.Paths {
		if MatchMemoryPath(pattern, rel) {
			return true
		}
	}
	return false
}

// MemoryMatchesKeyword returns the first when.keywords entry contained in text
// (case-insensitive), for the P1b UserPromptSubmit injection.
func MemoryMatchesKeyword(meta MemoryMeta, text string) (string, bool) {
	if meta.When == nil {
		return "", false
	}
	lower := strings.ToLower(text)
	for _, kw := range meta.When.Keywords {
		if k := strings.ToLower(strings.TrimSpace(kw)); k != "" && strings.Contains(lower, k) {
			return kw, true
		}
	}
	return "", false
}

// MemoryMatchesCommand returns the first when.commands prefix of command (P5).
func MemoryMatchesCommand(meta MemoryMeta, command string) (string, bool) {
	if meta.When == nil {
		return "", false
	}
	command = strings.TrimSpace(command)
	for _, prefix := range meta.When.Commands {
		if p := strings.TrimSpace(prefix); p != "" && strings.HasPrefix(command, p) {
			return prefix, true
		}
	}
	return "", false
}
