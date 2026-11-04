// Callers stored in the database (配置入库 P1): the model side of `auth_callers`
// (design docs/design/2026-11-03-config-into-db-design.md, plan
// docs/plans/2026-11-03-caller-store-p1-plan.md T2).
//
// The configuration FILE stays the highest authority: an entry declared there always
// wins, and a stored row that names the same id is hidden (and, on the server path,
// taken over — see T3). Everything here is additive: ApplyStoredCallers only APPENDS
// callers, it never edits or removes a file-declared one, and it records the source so
// the write path can project the file back exactly as it was (fileProjection).
package config

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	yaml "github.com/goccy/go-yaml"
)

// CallerSourceDB marks a caller that came from the auth_callers table instead of the
// configuration file. The zero value ("") is a FILE caller, so every existing fixture and
// every config decoded from a file keeps reading as operator-declared without changes.
const CallerSourceDB = "db"

// storedCallerIDPattern is the id rule the caller store shares with worker ids
// (^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$): an id ends up in paths, audit lines and job
// owners, so it must be filename- and log-safe.
var storedCallerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// reservedCallerIDs are ids a stored caller may not take: `default` is the implicit
// single-token caller, `system`/`root` are identity words the audit and the job layer
// use, and the `job-` prefix belongs to job credentials (a job's CallerID IS its job id).
var reservedCallerIDs = map[string]bool{"default": true, "system": true, "root": true}

const reservedCallerPrefix = "job-"

// ValidCallerID reports whether id may name a stored caller.
func ValidCallerID(id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("caller id is required")
	}
	if !storedCallerIDPattern.MatchString(id) {
		return fmt.Errorf("caller id must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	if reservedCallerIDs[strings.ToLower(id)] {
		return fmt.Errorf("caller id %q is reserved", id)
	}
	if strings.HasPrefix(strings.ToLower(id), reservedCallerPrefix) {
		return fmt.Errorf("caller id %q must not start with %q (that prefix belongs to job credentials)", id, reservedCallerPrefix)
	}
	return nil
}

// ValidCallerName reports whether name is a usable display name: at most
// MaxCallerNameLen characters and free of control characters (it is printed, logged and
// shown in the console).
func ValidCallerName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if utf8.RuneCountInString(name) > MaxCallerNameLen {
		return fmt.Errorf("caller name is longer than %d characters", MaxCallerNameLen)
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("caller name contains a control character")
		}
	}
	return nil
}

// StoredCallerInput is the config layer's view of one auth_callers row (the jobstore
// StoredCaller, restated here so config needs no jobstore import — G022).
type StoredCallerInput struct {
	ID           string
	Name         string
	TokenHash    string
	TokenHint    string
	LimitsJSON   string
	CeilingYAML  string
	DisabledAt   int64
	SupersededAt int64
}

// CallerLimits is the stored quota block (auth_callers.limits_json).
type CallerLimits struct {
	MaxConcurrentJobs int     `json:"max_concurrent_jobs,omitempty"`
	RateLimit         float64 `json:"rate_limit,omitempty"`
	RateBurst         int     `json:"rate_burst,omitempty"`
}

// DecodeCallerLimits decodes a stored quota block; empty means "no override".
func DecodeCallerLimits(s string) (CallerLimits, error) {
	var lim CallerLimits
	s = strings.TrimSpace(s)
	if s == "" {
		return lim, nil
	}
	if err := json.Unmarshal([]byte(s), &lim); err != nil {
		return CallerLimits{}, fmt.Errorf("quota block is not valid JSON: %w", err)
	}
	if lim.MaxConcurrentJobs < 0 {
		return CallerLimits{}, fmt.Errorf("max_concurrent_jobs must not be negative")
	}
	if lim.RateLimit < 0 {
		return CallerLimits{}, fmt.Errorf("rate_limit must not be negative")
	}
	if lim.RateBurst < 0 {
		return CallerLimits{}, fmt.Errorf("rate_burst must not be negative")
	}
	return lim, nil
}

// DecodeCallerCeiling decodes a stored ceiling block STRICTLY (an unknown key is a typo
// that would otherwise read as "no such restriction") and refuses an empty block, the
// same rule the loader applies to a file-declared `ceiling:` (D16).
func DecodeCallerCeiling(s string) (*CallerCeiling, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return nil, fmt.Errorf("ceiling is empty: list what the caller may do, or store no ceiling")
	}
	var probe map[string]any
	if err := yaml.Unmarshal([]byte(raw), &probe); err != nil {
		return nil, fmt.Errorf("ceiling is not valid YAML: %w", err)
	}
	if len(probe) == 0 {
		return nil, fmt.Errorf("ceiling is empty: list what the caller may do, or store no ceiling")
	}
	var ceil CallerCeiling
	if err := yaml.UnmarshalWithOptions([]byte(raw), &ceil, yaml.DisallowUnknownField()); err != nil {
		return nil, fmt.Errorf("ceiling: %w (the ceiling block is decoded strictly)", err)
	}
	if err := validateCeilingGlobs("ceiling", &ceil); err != nil {
		return nil, err
	}
	return &ceil, nil
}

// lockedCeilingGlob matches nothing a caller could ask for: it is the value the offline
// repair writes into a ceiling it cannot decode, so the repaired caller is locked out of
// every project / runner / worker / agent instead of being left unbounded.
const lockedCeilingGlob = "__gofer_locked_no_match__"

// LockedCallerCeiling is the most restrictive ceiling: nothing matches, read-only, and no
// exec and no global memory.
func LockedCallerCeiling() *CallerCeiling {
	no, yes := false, true
	return &CallerCeiling{
		Projects:     []string{lockedCeilingGlob},
		Runners:      []string{lockedCeilingGlob},
		Workers:      []string{lockedCeilingGlob},
		Agents:       []string{lockedCeilingGlob},
		AllowExec:    clonePtr(&no),
		GlobalMemory: clonePtr(&no),
		ReadOnly:     clonePtr(&yes),
	}
}

// LockedCallerCeilingYAML renders LockedCallerCeiling for storage (the repair command
// needs the wire form).
func LockedCallerCeilingYAML() string {
	b, err := yaml.Marshal(LockedCallerCeiling())
	if err != nil {
		return "read_only: true\n"
	}
	return string(b)
}

// validateCeilingGlobs holds a ceiling to the loader's rules on its own (no Config
// needed): every pattern must be a legal path.Match glob and must not be empty, because a
// typo would silently match nothing (a lock-out) or, worse, read as no limit at all.
func validateCeilingGlobs(owner string, ceil *CallerCeiling) error {
	if ceil == nil {
		return nil
	}
	for _, dim := range []struct {
		key      string
		patterns []string
	}{
		{"projects", ceil.Projects},
		{"runners", ceil.Runners},
		{"workers", ceil.Workers},
		{"agents", ceil.Agents},
	} {
		for _, pattern := range dim.patterns {
			if strings.TrimSpace(pattern) == "" {
				return fmt.Errorf("%s.%s: empty pattern", owner, dim.key)
			}
			if _, err := path.Match(pattern, "sample"); err != nil {
				return fmt.Errorf("%s.%s: invalid pattern %q: %w", owner, dim.key, pattern, err)
			}
		}
	}
	return nil
}

// MergeResult reports what ApplyStoredCallers did beyond appending rows.
type MergeResult struct {
	// Superseded lists the stored rows that a configuration-file entry (or a worker
	// binding) with the same id hides. The server path takes them over (disables them and
	// orphans their grants); the read-only path only hides them in memory.
	Superseded []StoredCallerInput
	// Warnings are the load-time notices the merge produced (a dropped disabled row, a
	// cleared duplicate name).
	Warnings []string
}

// ApplyStoredCallers appends the stored callers to cfg's caller list, in the order given.
// It never edits or removes a file-declared caller: the file wins, so a stored row naming
// the same id is reported in MergeResult.Superseded instead of being appended.
//
// A stored row is held to the SAME field checks the loader applies to a file entry (D17),
// so the merged configuration can always pass Validate: an enabled row with an unusable
// token hash, id, ceiling, quota or name is an ERROR naming the caller (the server must
// refuse to start rather than run a caller whose limits cannot be trusted), a disabled row
// with an unusable id is dropped with a warning (such a row can only come from a hand
// edited database, and its id cannot name any job), and a display name that collides with
// another caller's is CLEARED with a warning (the name is optional and display-only).
func ApplyStoredCallers(cfg *Config, rows []StoredCallerInput) (MergeResult, error) {
	var res MergeResult
	if cfg == nil {
		return res, nil
	}
	fileIDs := make(map[string]bool, len(cfg.Server.Callers))
	for _, cc := range cfg.Server.Callers {
		if cc.ID != "" {
			fileIDs[cc.ID] = true
		}
	}
	names := make(map[string]string, len(cfg.Server.Callers))
	for _, cc := range cfg.Server.Callers {
		if name := strings.ToLower(strings.TrimSpace(cc.Name)); name != "" && cc.ID != "" {
			names[name] = cc.ID
		}
	}
	for _, row := range rows {
		id := strings.TrimSpace(row.ID)
		disabled := row.DisabledAt != 0
		if err := ValidCallerID(id); err != nil {
			if disabled {
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"auth_callers %q (disabled): dropped: %v", id, err))
				continue
			}
			return res, fmt.Errorf("auth_callers %q: %w", id, err)
		}
		if _, clash := cfg.Server.Workers[id]; clash {
			// A stored caller must never share an id with a worker binding: the worker
			// credential would shadow the caller's token and the jobs of both would carry
			// one owner id. The id stays blocked (OwnerBlocked) so its work cannot start.
			cfg.markOwnerBlocked(id)
			res.Superseded = append(res.Superseded, row)
			continue
		}
		if fileIDs[id] {
			res.Superseded = append(res.Superseded, row)
			continue
		}
		entry, warnings, err := storedCallerEntry(row)
		if err != nil {
			return res, err
		}
		res.Warnings = append(res.Warnings, warnings...)
		if name := strings.ToLower(strings.TrimSpace(entry.Name)); name != "" {
			if owner, dup := names[name]; dup && owner != id {
				entry.Name = ""
				res.Warnings = append(res.Warnings, fmt.Sprintf(
					"auth_callers %q: display name %q is also used by caller %q; the stored caller keeps no name",
					id, strings.TrimSpace(row.Name), owner))
			} else {
				names[name] = id
			}
		}
		cfg.Server.Callers = append(cfg.Server.Callers, entry)
	}
	cfg.storedCallerWarnings = storedCallerCeilingWarnings(cfg)
	return res, nil
}

// storedCallerEntry validates one stored row and projects it onto a CallerConfig.
func storedCallerEntry(row StoredCallerInput) (CallerConfig, []string, error) {
	id := strings.TrimSpace(row.ID)
	disabled := row.DisabledAt != 0
	entry := CallerConfig{
		ID:        id,
		Name:      strings.TrimSpace(row.Name),
		Source:    CallerSourceDB,
		TokenHash: strings.TrimSpace(row.TokenHash),
		TokenHint: strings.TrimSpace(row.TokenHint),
		Disabled:  disabled,
		// A stored caller is NEVER a root administrator: can_admin is the operator's file
		// key for the identity that must not be editable from the console, so a database
		// row cannot claim it whatever a hand-edited database says.
		CanAdmin: false,
	}
	var warnings []string
	if err := ValidCallerName(entry.Name); err != nil {
		if disabled {
			entry.Name = ""
			warnings = append(warnings, fmt.Sprintf("auth_callers %q (disabled): %v; the name is cleared", id, err))
		} else {
			return CallerConfig{}, nil, fmt.Errorf("auth_callers %q: %w", id, err)
		}
	}
	if !disabled {
		// A disabled row is never authenticated, so its hash does not have to be usable;
		// enabling it re-runs this merge (which then reports a bad hash — the operator must
		// rotate first).
		if !isHexSHA256(entry.TokenHash) {
			return CallerConfig{}, nil, fmt.Errorf("auth_callers %q: token hash is not a 64-character hex sha256", id)
		}
	}
	if strings.TrimSpace(row.CeilingYAML) != "" {
		ceil, err := DecodeCallerCeiling(row.CeilingYAML)
		if err != nil {
			return CallerConfig{}, nil, fmt.Errorf("auth_callers %q: %w", id, err)
		}
		if err := validateCeilingGlobs(fmt.Sprintf("auth_callers[%q].ceiling", id), ceil); err != nil {
			return CallerConfig{}, nil, err
		}
		entry.Ceiling = ceil
	}
	limits, err := DecodeCallerLimits(row.LimitsJSON)
	if err != nil {
		return CallerConfig{}, nil, fmt.Errorf("auth_callers %q: %w", id, err)
	}
	entry.MaxConcurrentJobs = limits.MaxConcurrentJobs
	entry.RateLimit = limits.RateLimit
	entry.RateBurst = limits.RateBurst
	return entry, warnings, nil
}

// isHexSHA256 reports whether h is a lowercase or uppercase hex sha256 (64 hex digits).
func isHexSHA256(h string) bool {
	if len(h) != 64 {
		return false
	}
	_, err := hex.DecodeString(h)
	return err == nil
}

// IsStoredCaller reports whether cc came from the database rather than the file.
func (cc CallerConfig) IsStoredCaller() bool { return cc.Source == CallerSourceDB }

// FileCallers returns the file-declared callers (the ones a save may write back).
func (sc *ServerConfig) FileCallers() []CallerConfig {
	if sc == nil {
		return nil
	}
	out := make([]CallerConfig, 0, len(sc.Callers))
	for _, cc := range sc.Callers {
		if !cc.IsStoredCaller() {
			out = append(out, cc)
		}
	}
	return out
}

// OwnerBlocked reports whether id must be refused as a JOB OWNER: it names a disabled
// caller, or an id the caller merge had to block (a stored caller sharing a worker's id).
// The job layer's admission, re-check and owner inheritance all ask this one question.
func (c *Config) OwnerBlocked(id string) bool {
	if c == nil || id == "" {
		return false
	}
	if _, blocked := c.callerBlockedOwners[id]; blocked {
		return true
	}
	for _, cc := range c.Server.Callers {
		if cc.ID == id {
			return cc.Disabled
		}
	}
	return false
}

// markOwnerBlocked records an id whose jobs must never start (worker id collision).
func (c *Config) markOwnerBlocked(id string) {
	if c == nil || id == "" {
		return
	}
	if c.callerBlockedOwners == nil {
		c.callerBlockedOwners = make(map[string]bool, 1)
	}
	c.callerBlockedOwners[id] = true
}

// StoredCallerIDs returns the ids of the callers that came from the database, in caller
// order.
func (sc *ServerConfig) StoredCallerIDs() []string {
	if sc == nil {
		return nil
	}
	var out []string
	for _, cc := range sc.Callers {
		if cc.IsStoredCaller() && cc.ID != "" {
			out = append(out, cc.ID)
		}
	}
	return out
}

// HasStoreMarker reports whether the configuration document carries the P2 store marker
// (`config_store`), the flag that says "the entries of this deployment live in the
// database". The question is asked of the KEY as written — in any file the config was read
// from, INCLUDING an `include` fragment — which is why it goes through the source map and
// not SectionFile (that one answers for a key the config does not have).
func (c *Config) HasStoreMarker() bool { return c.HasTopKey("config_store") }

// ClearStoredCallers removes every caller that came from the database (and the state a
// merge produced with it: the blocked-owner set and the stored-caller warnings). The
// caller write path calls it before re-merging from the rows its transaction just wrote,
// so the second merge starts from exactly the FILE callers.
func (c *Config) ClearStoredCallers() {
	if c == nil {
		return
	}
	kept := c.Server.FileCallers()
	if len(kept) == 0 {
		kept = nil
	}
	c.Server.Callers = kept
	c.callerBlockedOwners = nil
	c.storedCallerWarnings = nil
}

// fileProjection returns a copy of cfg whose caller list holds only the FILE callers, so a
// save renders exactly the entries the operator wrote. When nothing came from the database
// the config is returned unchanged (no copy, byte-identical save); the projected list
// collapses to nil when every caller came from the database, so the renderer leaves the
// key out exactly like a config that never had one.
func fileProjection(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	kept := cfg.Server.FileCallers()
	if len(kept) == len(cfg.Server.Callers) {
		return cfg
	}
	if len(kept) == 0 {
		kept = nil
	}
	out := cfg.Clone()
	out.Server.Callers = kept
	return out
}
