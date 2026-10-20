// Package settings is the extension-settings registry and service: runtime-adjustable
// knobs (prompts, thresholds, exclusion lists) stored in the database instead of
// config.yaml, so a change applies immediately without restarting the server.
//
// Design: docs/design/2026-10-20-work-noise-and-db-settings-design.md. The split from
// config.yaml is deliberate and not to be blurred: what affects startup, security
// boundaries, connectivity or permissions stays in the configuration file; what only
// changes an agent's wording, a threshold or a list belongs here, because it must take
// effect at once and a bad edit must be revertible at once.
//
// Every key is registered in this package (name, type, default, description, validation
// and bounds, whether it is multiline). The registry is the single source of truth: a
// value that is not registered cannot be stored, and the HTTP / CLI entry points render
// the registry as-is (the web settings page is generated from it). The service reads
// "the database value, else the default" and caches the snapshot by generation:
//
//   - a write of THIS process is visible to the next read at once (the cache is refreshed
//     on the write path),
//   - a write of ANOTHER process is picked up within the cache TTL (5s by default), the
//     same contract the authorization cache has (auth_meta.grants_version).
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/inhere/gofer/internal/jobstore"
)

// Kind is the type of a setting's value. The kind decides how the value is stored (its
// text form) and what Set accepts.
type Kind string

const (
	// KindString is free text. max_len bounds it; Multiline marks the prompt keys the web
	// editor opens in a textarea.
	KindString Kind = "string"
	// KindInt is an integer in [Min, Max].
	KindInt Kind = "int"
	// KindBool is true / false.
	KindBool Kind = "bool"
	// KindStrings is a list of strings (project keys, cwd prefixes, …).
	KindStrings Kind = "strings"
	// KindPatterns is a list of regular expressions, compiled on save: an expression that
	// does not compile is refused with the compiler's reason.
	KindPatterns Kind = "patterns"
)

// Valid reports whether k is a kind the registry may use.
func (k Kind) Valid() bool {
	switch k {
	case KindString, KindInt, KindBool, KindStrings, KindPatterns:
		return true
	}
	return false
}

// Groups group the keys by subsystem in the listings (the web page renders one section per
// group).
const (
	GroupWork   = "work"
	GroupPrompt = "prompt"
)

// MaxPromptLen caps a prompt setting (32 KiB): a prompt is sent with every summarize /
// steward run, so a runaway paste must not bloat every job.
const MaxPromptLen = 32 * 1024

// Definition registers one setting. It is the single description of a key: the default,
// the validation, and everything the listings show.
type Definition struct {
	// Key is `<subsystem>.<name>`, e.g. `work.draft.min_prompts`.
	Key string
	// Group is the subsystem the key is listed under.
	Group string
	// Kind is the value type.
	Kind Kind
	// Default is the value used when the key has no database row (same Go type as Kind:
	// string, int, bool, []string).
	Default any
	// Description says what the setting does, in Chinese, in one sentence.
	Description string
	// Min / Max bound an int setting (inclusive).
	Min int
	Max int
	// MaxLen bounds a string setting in bytes.
	MaxLen int
	// Multiline marks a long text (a prompt) the editors open in a textarea.
	Multiline bool
}

// Registry keys. Handlers, planners and tests spell keys through these constants rather
// than repeating string literals (the names are published, they never change).
const (
	KeyWorkDraftMinPrompts           = "work.draft.min_prompts"
	KeyWorkDraftExcludeProjects      = "work.draft.exclude_projects"
	KeyWorkDraftExcludeCwds          = "work.draft.exclude_cwds"
	KeyWorkDraftExcludeTitlePatterns = "work.draft.exclude_title_patterns"
	KeyWorkShortSessionMaxMinutes    = "work.short_session.max_minutes"
	KeyWorkShortSessionMaxPrompts    = "work.short_session.max_prompts"
	KeyPromptStewardRole             = "prompt.steward.role"
	KeyPromptStewardRoleExtra        = "prompt.steward.role.extra"
	KeyPromptStewardReview           = "prompt.steward.review"
	KeyPromptStewardReviewExtra      = "prompt.steward.review.extra"
	KeyPromptWorkSummarizer          = "prompt.work.summarizer"
	KeyPromptWorkSummarizerExtra     = "prompt.work.summarizer.extra"
)

// registry is the key table, in listing order. The first batch is the noise-reduction
// knobs and the three prompt overrides of docs/design/2026-10-20-work-noise-and-db-settings
// -design.md; the consumers read them through their own narrow Settings seam (the work
// noise rules and the summarizer prompt in internal/work, the steward prompts in
// internal/steward), so a write applies to the next prompt / tick.
var registry = []Definition{
	{
		Key: KeyWorkDraftMinPrompts, Group: GroupWork, Kind: KindInt, Default: 2,
		Min: 1, Max: 20,
		Description: "同一会话第几次人工提问才建工作项草稿（1 = 第一次就建）",
	},
	{
		Key: KeyWorkDraftExcludeProjects, Group: GroupWork, Kind: KindStrings, Default: []string{},
		Description: "不建草稿的项目 key 名单（精确匹配）",
	},
	{
		Key: KeyWorkDraftExcludeCwds, Group: GroupWork, Kind: KindStrings, Default: []string{},
		Description: "不建草稿的目录名单（会话 cwd 以其中某一项为前缀即命中）",
	},
	{
		Key: KeyWorkDraftExcludeTitlePatterns, Group: GroupWork, Kind: KindPatterns, Default: []string{},
		Description: "不建草稿的标题正则（会话标题匹配任意一条即命中）",
	},
	{
		Key: KeyWorkShortSessionMaxMinutes, Group: GroupWork, Kind: KindInt, Default: 10,
		Min: 0, Max: 1440,
		Description: "短命会话的时长阈值（分钟；已结束且存活短于此、提问次数也少于此的会话不进整理与管家）",
	},
	{
		Key: KeyWorkShortSessionMaxPrompts, Group: GroupWork, Kind: KindInt, Default: 3,
		Min: 0, Max: 100,
		Description: "短命会话的提问次数阈值（人工提问少于此且存活时间也短的会话不进整理与管家）",
	},
	{
		Key: KeyPromptStewardRole, Group: GroupPrompt, Kind: KindString, Default: "",
		MaxLen: MaxPromptLen, Multiline: true,
		Description: "管家角色提示词的整段替换（为空 = 用代码里的默认）",
	},
	{
		Key: KeyPromptStewardRoleExtra, Group: GroupPrompt, Kind: KindString, Default: "",
		MaxLen: MaxPromptLen, Multiline: true,
		Description: "追加在管家角色提示词之后的额外要求（推荐用它写偏好，跟得上默认提示词更新）",
	},
	{
		Key: KeyPromptStewardReview, Group: GroupPrompt, Kind: KindString, Default: "",
		MaxLen: MaxPromptLen, Multiline: true,
		Description: "管家巡检提示词的整段替换（为空 = 用代码里的默认）",
	},
	{
		Key: KeyPromptStewardReviewExtra, Group: GroupPrompt, Kind: KindString, Default: "",
		MaxLen: MaxPromptLen, Multiline: true,
		Description: "追加在管家巡检提示词之后的额外要求",
	},
	{
		Key: KeyPromptWorkSummarizer, Group: GroupPrompt, Kind: KindString, Default: "",
		MaxLen: MaxPromptLen, Multiline: true,
		Description: "整理器提示词的整段替换（为空 = 用代码里的默认；要求的 JSON 输出格式说明总由代码追加在最后，替换不掉）",
	},
	{
		Key: KeyPromptWorkSummarizerExtra, Group: GroupPrompt, Kind: KindString, Default: "",
		MaxLen: MaxPromptLen, Multiline: true,
		Description: "追加在整理器提示词之后的额外要求（推荐的调整方式）",
	},
}

// ErrUnknownKey is returned for a key that is not registered.
var ErrUnknownKey = errors.New("settings: unknown key")

// ValidationError is a refused value, with the reason a person can act on.
type ValidationError struct {
	Key    string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("settings: %s: %s", e.Key, e.Reason)
}

func invalid(key, format string, args ...any) error {
	return &ValidationError{Key: key, Reason: fmt.Sprintf(format, args...)}
}

// Definitions returns the registry in listing order (a copy: callers may sort it).
func Definitions() []Definition {
	out := make([]Definition, len(registry))
	copy(out, registry)
	return out
}

// Lookup returns the definition of key.
func Lookup(key string) (Definition, bool) {
	for _, d := range registry {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

// Item is one key as a listing shows it: the definition plus the effective and stored
// values.
type Item struct {
	Key         string `json:"key"`
	Group       string `json:"group"`
	Kind        Kind   `json:"kind"`
	Description string `json:"description"`
	Default     any    `json:"default"`
	// Value is the effective value: the stored one, else the default.
	Value any `json:"value"`
	// Modified reports whether the key has a stored value (that is, it was changed from —
	// or explicitly set to — the default).
	Modified  bool `json:"modified"`
	Multiline bool `json:"multiline,omitempty"`
	Min       *int `json:"min,omitempty"`
	Max       *int `json:"max,omitempty"`
	MaxLen    int  `json:"max_len,omitempty"`
	// UpdatedBy / UpdatedAt describe the last write of a modified key.
	UpdatedBy string `json:"updated_by,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

// Change is one key of an import: what the file asks for and what the store says.
type Change struct {
	Key string `json:"key"`
	// Action is "set", "reset" (the file removes the key / leaves it empty and the key is
	// currently modified) or "unchanged".
	Action string `json:"action"`
	Old    any    `json:"old"`
	New    any    `json:"new"`
	Error  string `json:"error,omitempty"`
}

// ImportResult is the answer of Import: the per-key differences and whether they were
// written (dry run leaves everything untouched, including the ones that failed
// validation).
type ImportResult struct {
	DryRun  bool     `json:"dry_run"`
	Applied bool     `json:"applied"`
	Changes []Change `json:"changes"`
	// Errors counts the refused keys (they are also marked in Changes).
	Errors int `json:"errors"`
}

// Service reads and writes the settings through a job store, with a generation-keyed
// cache.
type Service struct {
	store *jobstore.Store

	// ttl is how long a cached snapshot is trusted without re-reading the generation
	// (defaultCacheTTL). A write of this process refreshes the cache directly, so only
	// another process's writes wait for the ttl.
	ttl time.Duration
	// now is the clock (tests).
	now func() time.Time

	mu    sync.Mutex
	cache atomic.Pointer[cacheEntry]
}

// defaultCacheTTL is the cross-process freshness window: the same 5 seconds the
// authorization cache uses, so "changed it, wait a moment" has one number in the product.
const defaultCacheTTL = 5 * time.Second

type cacheEntry struct {
	version int64
	at      time.Time
	values  map[string]string
}

// NewService builds the service over store. store may be nil for a server without a
// database (MCP / bare tests): reads answer with the defaults and writes fail.
func NewService(store *jobstore.Store) *Service {
	return &Service{store: store, ttl: defaultCacheTTL, now: time.Now}
}

// SetCacheTTL overrides the cross-process freshness window (tests shrink it).
func (s *Service) SetCacheTTL(d time.Duration) { s.ttl = d }

// SetClock overrides the clock (tests).
func (s *Service) SetClock(fn func() time.Time) { s.now = fn }

// GetString returns the effective string value of key.
func (s *Service) GetString(key string) (string, error) {
	d, v, err := s.get(key)
	if err != nil {
		return "", err
	}
	str, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("settings: %s is a %s, not a string", d.Key, d.Kind)
	}
	return str, nil
}

// GetInt returns the effective int value of key.
func (s *Service) GetInt(key string) (int, error) {
	d, v, err := s.get(key)
	if err != nil {
		return 0, err
	}
	n, ok := v.(int)
	if !ok {
		return 0, fmt.Errorf("settings: %s is a %s, not an int", d.Key, d.Kind)
	}
	return n, nil
}

// GetBool returns the effective bool value of key.
func (s *Service) GetBool(key string) (bool, error) {
	d, v, err := s.get(key)
	if err != nil {
		return false, err
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("settings: %s is a %s, not a bool", d.Key, d.Kind)
	}
	return b, nil
}

// GetStrings returns the effective list value of key (string and regex lists alike).
func (s *Service) GetStrings(key string) ([]string, error) {
	d, v, err := s.get(key)
	if err != nil {
		return nil, err
	}
	list, ok := v.([]string)
	if !ok {
		return nil, fmt.Errorf("settings: %s is a %s, not a list", d.Key, d.Kind)
	}
	return list, nil
}

// Patterns returns the compiled regular expressions of a KindPatterns key. Saving already
// refused anything that does not compile, so an error here means a stored value was
// edited outside the service.
func (s *Service) Patterns(key string) ([]*regexp.Regexp, error) {
	d, err := s.def(key)
	if err != nil {
		return nil, err
	}
	if d.Kind != KindPatterns {
		return nil, fmt.Errorf("settings: %s is a %s, not a pattern list", d.Key, d.Kind)
	}
	list, err := s.GetStrings(key)
	if err != nil {
		return nil, err
	}
	out := make([]*regexp.Regexp, 0, len(list))
	for _, expr := range list {
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("settings: %s holds an invalid pattern %q: %w", key, expr, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// Get returns the effective value of key (string, int, bool or []string).
func (s *Service) Get(key string) (any, error) {
	_, v, err := s.get(key)
	return v, err
}

func (s *Service) def(key string) (Definition, error) {
	d, ok := Lookup(key)
	if !ok {
		return Definition{}, fmt.Errorf("%w: %q", ErrUnknownKey, key)
	}
	return d, nil
}

func (s *Service) get(key string) (Definition, any, error) {
	d, err := s.def(key)
	if err != nil {
		return Definition{}, nil, err
	}
	values, err := s.values()
	if err != nil {
		return Definition{}, nil, err
	}
	raw, ok := values[key]
	if !ok {
		return d, d.Default, nil
	}
	v, err := decode(d, raw)
	if err != nil {
		return Definition{}, nil, err
	}
	return d, v, nil
}

// Set validates v against key's definition and stores it. It returns the item as a listing
// shows it (effective value, modified=true) and the new generation.
func (s *Service) Set(key string, v any, by string) (Item, int64, error) {
	d, err := s.def(key)
	if err != nil {
		return Item{}, 0, err
	}
	raw, err := encode(d, v)
	if err != nil {
		return Item{}, 0, err
	}
	if s.store == nil {
		return Item{}, 0, errors.New("settings: no store wired")
	}
	ver, err := s.store.PutSetting(key, raw, by)
	if err != nil {
		return Item{}, 0, err
	}
	s.refresh()
	item, err := s.item(d)
	if err != nil {
		return Item{}, ver, err
	}
	return item, ver, nil
}

// Reset deletes the stored value of key, so the registry default applies again. It reports
// whether a value was stored (a reset of an unmodified key is a no-op).
func (s *Service) Reset(key string) (bool, error) {
	if _, err := s.def(key); err != nil {
		return false, err
	}
	if s.store == nil {
		return false, errors.New("settings: no store wired")
	}
	existed, _, err := s.store.DeleteSetting(key)
	if err != nil {
		return false, err
	}
	if existed {
		s.refresh()
	}
	return existed, nil
}

// List returns every registered key with its effective and stored values, in registry
// order.
func (s *Service) List() ([]Item, error) {
	rows, err := s.stored()
	if err != nil {
		return nil, err
	}
	out := make([]Item, 0, len(registry))
	for _, d := range registry {
		item := Item{
			Key: d.Key, Group: d.Group, Kind: d.Kind, Description: d.Description,
			Default: d.Default, Multiline: d.Multiline, MaxLen: d.MaxLen,
		}
		if d.Kind == KindInt {
			mn, mx := d.Min, d.Max
			item.Min, item.Max = &mn, &mx
		}
		if st, ok := rows[d.Key]; ok {
			v, err := decode(d, st.Value)
			if err != nil {
				return nil, err
			}
			item.Value, item.Modified = v, true
			item.UpdatedBy, item.UpdatedAt = st.UpdatedBy, st.UpdatedAt
		} else {
			item.Value = d.Default
		}
		out = append(out, item)
	}
	return out, nil
}

// Export renders the MODIFIED keys as YAML, one `key: <value>` line per key (a value in
// JSON form, which is valid YAML too). Only modified keys are exported, so an export /
// import round trip carries the operator's changes and nothing that is the code default —
// the defaults follow the binary, exactly like a key nobody touched.
func (s *Service) Export() ([]byte, error) {
	rows, err := s.stored()
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(rows))
	for key := range rows {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# gofer extension settings (modified keys only); import with `gofer settings import`\n")
	for _, key := range keys {
		d, ok := Lookup(key)
		if !ok {
			continue
		}
		v, err := decode(d, rows[key].Value)
		if err != nil {
			return nil, err
		}
		enc, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%s: %s\n", key, enc)
	}
	return []byte(b.String()), nil
}

// Import applies (or, with dryRun, only reports) the keys of a YAML document. Every key is
// validated on its own: a refused key is reported and left alone, the others still apply.
// A key whose value is null or an empty string is reset to the default.
func (s *Service) Import(document []byte, dryRun bool, by string) (ImportResult, error) {
	decodeYAML := yamlUnmarshal
	var doc map[string]any
	if err := decodeYAML(document, &doc); err != nil {
		return ImportResult{}, fmt.Errorf("settings: parse import document: %w", err)
	}
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	res := ImportResult{DryRun: dryRun, Changes: make([]Change, 0, len(keys))}
	for _, key := range keys {
		change := s.importKey(key, doc[key], dryRun, by)
		if change.Error != "" {
			res.Errors++
		}
		res.Changes = append(res.Changes, change)
	}
	res.Applied = !dryRun && res.Errors == 0
	return res, nil
}

// importKey validates and, unless dry-run, writes one key of an import document.
func (s *Service) importKey(key string, value any, dryRun bool, by string) Change {
	d, ok := Lookup(key)
	if !ok {
		return Change{Key: key, Action: "unchanged", Error: fmt.Sprintf("unknown key %q", key)}
	}
	item, err := s.item(d)
	if err != nil {
		return Change{Key: key, Action: "unchanged", Error: err.Error()}
	}
	change := Change{Key: key, Old: item.Value, New: item.Value}
	if isClear(value) {
		if !item.Modified {
			change.Action = "unchanged"
			return change
		}
		change.Action, change.New = "reset", d.Default
		if dryRun {
			return change
		}
		if _, err := s.Reset(key); err != nil {
			change.Error = err.Error()
		}
		return change
	}
	encoded, err := encode(d, normalizeImported(value))
	if err != nil {
		change.Error = err.Error()
		return change
	}
	decoded, err := decode(d, encoded)
	if err != nil {
		change.Error = err.Error()
		return change
	}
	change.New = decoded
	if fmt.Sprintf("%v", decoded) == fmt.Sprintf("%v", item.Value) && item.Modified {
		change.Action = "unchanged"
		return change
	}
	change.Action = "set"
	if dryRun {
		return change
	}
	if s.store == nil {
		change.Error = "no store wired"
		return change
	}
	if _, err := s.store.PutSetting(key, encoded, by); err != nil {
		change.Error = err.Error()
		return change
	}
	s.refresh()
	return change
}

// normalizeImported coerces the numeric types a YAML decoder may produce (uint64 / int64
// and a whole float64) into the forms encode accepts, so a document written by hand and a
// JSON body validate the same way.
func normalizeImported(v any) any {
	switch t := v.(type) {
	case uint64:
		return int(t)
	case uint:
		return int(t)
	case uint32:
		return int(t)
	case int64:
		return int(t)
	case int32:
		return int(t)
	case float64:
		if t == float64(int64(t)) {
			return int(t)
		}
		return t
	}
	return v
}

// isClear reports whether an imported value means "no value here" (the key falls back to
// its default).
func isClear(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	}
	return false
}

// item renders one key as a listing shows it.
func (s *Service) item(d Definition) (Item, error) {
	items, err := s.List()
	if err != nil {
		return Item{}, err
	}
	for _, it := range items {
		if it.Key == d.Key {
			return it, nil
		}
	}
	return Item{}, fmt.Errorf("settings: %s is not in the registry", d.Key)
}

// stored returns the stored rows keyed by setting key (no cache: writers use it).
func (s *Service) stored() (map[string]jobstore.Setting, error) {
	out := make(map[string]jobstore.Setting)
	if s.store == nil {
		return out, nil
	}
	rows, err := s.store.ListSettings()
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Key] = r
	}
	return out, nil
}

// values returns the cached raw values, reloading when the snapshot is stale (another
// process may have written) or the generation moved.
func (s *Service) values() (map[string]string, error) {
	now := s.now()
	if c := s.cache.Load(); c != nil && s.ttl > 0 && now.Sub(c.at) < s.ttl {
		return c.values, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.cache.Load(); c != nil && s.ttl > 0 && s.now().Sub(c.at) < s.ttl {
		return c.values, nil
	}
	if s.store == nil {
		return map[string]string{}, nil
	}
	version, err := s.store.SettingsVersion()
	if err != nil {
		return nil, err
	}
	if c := s.cache.Load(); c != nil && c.version == version {
		// Same generation: nothing was written, just renew the freshness window.
		s.cache.Store(&cacheEntry{version: version, at: now, values: c.values})
		return c.values, nil
	}
	rows, err := s.store.ListSettings()
	if err != nil {
		return nil, err
	}
	values := make(map[string]string, len(rows))
	for _, r := range rows {
		values[r.Key] = r.Value
	}
	s.cache.Store(&cacheEntry{version: version, at: now, values: values})
	return values, nil
}

// refresh reloads the snapshot after a write of this process, so the next read sees it at
// once (the cross-process ttl is irrelevant here).
func (s *Service) refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	version, err := s.store.SettingsVersion()
	if err != nil {
		s.cache.Store(nil)
		return
	}
	rows, err := s.store.ListSettings()
	if err != nil {
		s.cache.Store(nil)
		return
	}
	values := make(map[string]string, len(rows))
	for _, r := range rows {
		values[r.Key] = r.Value
	}
	s.cache.Store(&cacheEntry{version: version, at: s.now(), values: values})
}

// encode validates v against d and returns the stored text form.
func encode(d Definition, v any) (string, error) {
	switch d.Kind {
	case KindString:
		str, err := toString(d.Key, v)
		if err != nil {
			return "", err
		}
		if d.MaxLen > 0 && len(str) > d.MaxLen {
			return "", invalid(d.Key, "is longer than %d bytes", d.MaxLen)
		}
		return str, nil
	case KindInt:
		n, err := toInt(d.Key, v)
		if err != nil {
			return "", err
		}
		if n < d.Min || n > d.Max {
			return "", invalid(d.Key, "must be between %d and %d", d.Min, d.Max)
		}
		return strconv.Itoa(n), nil
	case KindBool:
		switch t := v.(type) {
		case bool:
			return strconv.FormatBool(t), nil
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(t))
			if err != nil {
				return "", invalid(d.Key, "must be true or false")
			}
			return strconv.FormatBool(b), nil
		}
		return "", invalid(d.Key, "must be true or false")
	case KindStrings, KindPatterns:
		list, err := toStringList(d.Key, v)
		if err != nil {
			return "", err
		}
		if d.Kind == KindPatterns {
			for _, expr := range list {
				if _, err := regexp.Compile(expr); err != nil {
					return "", invalid(d.Key, "pattern %q does not compile: %v", expr, err)
				}
			}
		}
		raw, err := json.Marshal(list)
		if err != nil {
			return "", invalid(d.Key, "cannot encode the list: %v", err)
		}
		return string(raw), nil
	}
	return "", invalid(d.Key, "has an unknown type %q", d.Kind)
}

// decode turns a stored text form back into the typed value.
func decode(d Definition, raw string) (any, error) {
	switch d.Kind {
	case KindString:
		return raw, nil
	case KindInt:
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("settings: stored value of %s is not an int: %w", d.Key, err)
		}
		return n, nil
	case KindBool:
		b, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("settings: stored value of %s is not a bool: %w", d.Key, err)
		}
		return b, nil
	case KindStrings, KindPatterns:
		var list []string
		if strings.TrimSpace(raw) == "" {
			list = []string{}
		} else if err := json.Unmarshal([]byte(raw), &list); err != nil {
			return nil, fmt.Errorf("settings: stored value of %s is not a list: %w", d.Key, err)
		}
		if list == nil {
			list = []string{}
		}
		return list, nil
	}
	return nil, fmt.Errorf("settings: %s has an unknown type %q", d.Key, d.Kind)
}

// toString coerces a JSON / YAML scalar into the string form.
func toString(key string, v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case int:
		return strconv.Itoa(t), nil
	case int64:
		return strconv.FormatInt(t, 10), nil
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10), nil
		}
		return strconv.FormatFloat(t, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", nil
	}
	return "", invalid(key, "must be text")
}

// toInt coerces a JSON / YAML scalar into an int, refusing fractions.
func toInt(key string, v any) (int, error) {
	switch t := v.(type) {
	case int:
		return t, nil
	case int64:
		return int(t), nil
	case float64:
		if t != float64(int64(t)) {
			return 0, invalid(key, "must be a whole number")
		}
		return int(t), nil
	case json.Number:
		n, err := t.Int64()
		if err != nil {
			return 0, invalid(key, "must be a whole number")
		}
		return int(n), nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, invalid(key, "must be a whole number")
		}
		return n, nil
	case bool:
		return 0, invalid(key, "must be a whole number")
	}
	return 0, invalid(key, "must be a whole number")
}

// toStringList accepts a YAML / JSON list, or the comma-separated text a person typed on
// the command line. Empty entries are dropped.
func toStringList(key string, v any) ([]string, error) {
	var raw []string
	switch t := v.(type) {
	case []string:
		raw = t
	case []any:
		for _, e := range t {
			str, err := toString(key, normalizeImported(e))
			if err != nil {
				return nil, err
			}
			raw = append(raw, str)
		}
	case string:
		for _, part := range strings.Split(t, ",") {
			raw = append(raw, part)
		}
	case nil:
		return []string{}, nil
	default:
		return nil, invalid(key, "must be a list of strings")
	}
	out := make([]string, 0, len(raw))
	for _, s := range raw {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}
