// Callers in the database: the assembly side (配置入库 P1, plan
// docs/plans/2026-11-03-caller-store-p1-plan.md T3).
//
// The config layer owns the model (config.ApplyStoredCallers appends the stored rows and
// validates them); this file owns the ORDER of the startup / reload merge and the one
// write transaction a merge may need:
//
//	read the stored rows   → a failure is fatal, never "no callers"
//	append + validate      → an unusable row fails the start / the reload
//	config.Validate        → the merged configuration must be acceptable
//	take over superseded   → one transaction: superseded_at + disabled_at, orphan the
//	                         `caller:<id>` grants, one audit line (a file entry with the
//	                         same id has taken the identity over)
//
// Nothing before the last step writes the database, so a merge that fails half way leaves
// no half-done takeover behind.
package core

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
)

// CallerMergeMode says whether the merge may write the database.
type CallerMergeMode int

const (
	// CallerMergeServer is the serve process (startup and reload): it may take over a
	// superseded stored caller.
	CallerMergeServer CallerMergeMode = iota
	// CallerMergeReadOnly is a path that must not write the database (standalone mcp):
	// a superseded row is hidden in memory only, and the next server start does the
	// takeover (design: the CLI does not write the database).
	CallerMergeReadOnly
)

// fileTakeoverActor is the audit actor of a takeover: no human did it, the configuration
// file did.
const fileTakeoverActor = "system:file-takeover"

// ErrStoreMarker is the refusal a configuration file carrying the P2 store marker gets
// from this version (D11). P1 cannot read a configuration whose entries live in the
// database, and continuing would silently run on whatever is left in the file.
var ErrStoreMarker = errors.New("this configuration has been migrated into the database; " +
	"this version cannot read it: restore the backup taken before the migration, or upgrade gofer")

// PrepareCallerStore is the startup seam: refuse a marked configuration BEFORE opening the
// database (opening one would create an empty database beside the operator's real one and
// a future version would migrate it), open the metadata store, merge the stored callers
// and hand the store to Build (core.WithStore).
//
// An error means the process must not start: a failed read of auth_callers is never read as
// "this deployment has no stored callers" (that would silently drop every stored identity
// and, on the next write, orphan nothing while the callers' tokens keep failing).
func PrepareCallerStore(cfg *config.Config, mode CallerMergeMode) (*jobstore.Store, error) {
	if cfg == nil {
		return nil, fmt.Errorf("callers: nil config")
	}
	if cfg.HasStoreMarker() {
		return nil, ErrStoreMarker
	}
	dbPath, err := cfg.ResolveDBPathChecked()
	if err != nil {
		return nil, err
	}
	store, err := jobstore.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open metadata store: %w", err)
	}
	if err := MergeStoredCallers(cfg, store, mode); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

// MergeStoredCallers appends the stored callers to cfg, validates the result and (on the
// server path) takes over the rows a file entry supersedes. It mutates cfg: callers of a
// LIVE config must pass a clone (the reload path does, and startup's cfg is not published
// yet).
func MergeStoredCallers(cfg *config.Config, store *jobstore.Store, mode CallerMergeMode) error {
	if cfg == nil {
		return nil
	}
	if store == nil {
		return fmt.Errorf("callers: no metadata store")
	}
	rows, err := store.ListStoredCallers()
	if err != nil {
		return fmt.Errorf("read stored callers: %w", err)
	}
	res, err := config.ApplyStoredCallers(cfg, storedCallerInputs(rows))
	if err != nil {
		return err
	}
	// D17: the merged configuration must be one the loader would have accepted, otherwise
	// every later write (including the ones that would fix it) fails on validation.
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("merged configuration: %w", err)
	}
	if mode != CallerMergeServer || len(res.Superseded) == 0 {
		return nil
	}
	return takeOverSupersededCallers(store, res.Superseded)
}

// takeOverSupersededCallers disables the stored callers a file entry has taken over, in ONE
// transaction: superseded_at + disabled_at, the `caller:<id>` grants orphaned, one audit
// line. A row that is already superseded is skipped, so a reload does not repeat it.
//
// D10 timestamps: a takeover stamp is max(now, rotated_at), so a later rotation is always
// strictly newer; enabling a taken-over row therefore requires a rotation that happened
// AFTER the takeover (the enable path compares the two).
func takeOverSupersededCallers(store *jobstore.Store, rows []config.StoredCallerInput) error {
	return store.WithCallerTx(func(tx jobstore.CallerTx) error {
		now := tx.Now()
		for _, row := range rows {
			if row.SupersededAt != 0 {
				continue // already taken over: nothing to record twice
			}
			cur, ok, err := tx.GetCaller(row.ID)
			if err != nil {
				return err
			}
			if !ok || cur.SupersededAt != 0 {
				continue
			}
			stamp := now
			if cur.RotatedAt > stamp {
				stamp = cur.RotatedAt
			}
			before := "stored caller is active"
			cur.SupersededAt = stamp
			if cur.DisabledAt == 0 {
				cur.DisabledAt = stamp
			}
			cur.UpdatedAt = now
			if err := tx.UpdateCallerTx(cur); err != nil {
				return err
			}
			grantIDs, err := tx.ActiveGrantIDs("caller:" + cur.ID)
			if err != nil {
				return err
			}
			if _, err := tx.OrphanGrants(grantIDs); err != nil {
				return err
			}
			if err := tx.InsertConfigAudit(jobstore.ConfigAuditEntry{
				At: now, Actor: fileTakeoverActor, Path: "server.callers." + cur.ID,
				BeforeMasked: before,
				AfterMasked:  fmt.Sprintf("superseded_at=%d disabled_at=%d (the configuration file declares this id)", stamp, cur.DisabledAt),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

// storedCallerInputs projects the store rows onto the config layer's own input type (config
// never imports jobstore — G022).
func storedCallerInputs(rows []jobstore.StoredCaller) []config.StoredCallerInput {
	out := make([]config.StoredCallerInput, 0, len(rows))
	for _, r := range rows {
		out = append(out, config.StoredCallerInput{
			ID:           r.ID,
			Name:         r.Name,
			TokenHash:    r.TokenHash,
			TokenHint:    r.TokenHint,
			LimitsJSON:   r.LimitsJSON,
			CeilingYAML:  r.CeilingYAML,
			DisabledAt:   r.DisabledAt,
			SupersededAt: r.SupersededAt,
		})
	}
	return out
}

// UpdateCallers is the caller write transaction (D4): it holds updateMu, clones the live
// config, lets fn write the caller rows and their audit lines, re-merges the DATABASE
// callers from the rows the transaction just wrote (so enabling a hidden row takes effect
// at once, and an unusable row fails here instead of at the next start), validates the
// candidate and only then publishes it.
//
// It NEVER calls saveConfig: a stored caller does not belong in config.yaml (the file is
// projected to its own callers by config.Save), so an add on a deployment without a config
// file must not create one.
func (c *Core) UpdateCallers(fn func(next *config.Config, tx jobstore.CallerTx) error) (*ConfigSnapshot, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("update callers: no metadata store")
	}
	c.updateMu.Lock()
	snap, err := c.updateCallersLocked(fn)
	c.updateMu.Unlock()
	if err != nil {
		return nil, err
	}
	c.flushPush()
	return snap, nil
}

// updateCallersLocked is UpdateCallers' body; the caller owns updateMu and the broadcast.
func (c *Core) updateCallersLocked(fn func(next *config.Config, tx jobstore.CallerTx) error) (*ConfigSnapshot, error) {
	if fn == nil {
		return nil, fmt.Errorf("update callers: nil callback")
	}
	next := c.snap.Load().Cfg.Clone()
	err := c.Store.WithCallerTx(func(tx jobstore.CallerTx) error {
		if err := fn(next, tx); err != nil {
			return err
		}
		// Re-merge from the rows this transaction wrote: the stored callers a clone of the
		// live config carries are stale by definition once fn has touched them.
		next.ClearStoredCallers()
		rows, err := tx.ListStoredCallers()
		if err != nil {
			return fmt.Errorf("read stored callers: %w", err)
		}
		res, err := config.ApplyStoredCallers(next, storedCallerInputs(rows))
		if err != nil {
			return err
		}
		// A row this write just hid behind a file entry (or a worker binding) is a lost
		// caller: the operator asked for something else and would only learn about it from a
		// list that no longer shows an active row. The write is refused instead (the add /
		// enable paths answer the same in their own words; this is the safety net).
		if id, hidden := firstNewlySuperseded(res.Superseded); hidden {
			return fmt.Errorf("%w: caller %q is declared in the configuration file or bound to a worker",
				config.ErrCallerIDTaken, id)
		}
		return c.validateCallerCandidate(next)
	})
	if err != nil {
		return nil, err
	}
	return c.reloadLocked(next), nil
}

// firstNewlySuperseded reports the id of a row that was ACTIVE before this merge and is hidden
// after it. A row that was already disabled or already superseded is expected to stay hidden
// (D10 / the worker-id collision case), so it is not an error.
func firstNewlySuperseded(rows []config.StoredCallerInput) (string, bool) {
	for _, row := range rows {
		if row.SupersededAt == 0 && row.DisabledAt == 0 {
			return row.ID, true
		}
	}
	return "", false
}

// ---- 用户管理（计划 T6）----
//
// Everything below is the WRITE and READ side of the caller administration the console and
// `gofer auth caller` drive. The entry layer (httpapi) binds a request and maps these results
// and errors onto HTTP; the order of every write lives here:
//
//	validate the input          → 400-class: nothing is touched
//	one caller transaction      → the row and its audit line land together
//	re-merge + validate         → the published snapshot is one the loader accepts
//
// A caller declared in the configuration FILE is not this API's to rotate, disable or enable
// (a rotation would have to write a plaintext token into the file); only its ceiling and quota
// are editable, through the ordinary config write transaction (D12).

// callerTokenPrefix is the fixed prefix of a minted caller token (D15), so a leaked string is
// recognisable in a scan / a log grep.
const callerTokenPrefix = "gct_"

// callerAuditPath is the config_audit path prefix of one caller (one row per change).
const callerAuditPath = "server.callers."

// mintCallerToken returns a fresh credential and its stored forms: 32 random bytes behind the
// `gct_` prefix, the lowercase hex sha256 the database keeps, and the hint `gct_…<last four>`
// a list shows. The token itself travels to exactly one response (design §「新增用户」): it is
// never stored, logged or audited.
func mintCallerToken() (token, hash, hint string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", "", fmt.Errorf("mint caller token: %w", err)
	}
	token = callerTokenPrefix + base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), callerTokenPrefix + "…" + token[len(token)-4:], nil
}

// AddCaller creates one stored caller and answers its token ONCE. The id must be unused by
// every identity of this deployment — a stored row (enabled, disabled or superseded), a
// configuration-file caller, or a worker binding — because ids are never reused: a fresh token
// under a retired id would inherit the old identity's grants.
func (c *Core) AddCaller(spec config.CallerSpec, actor string) (config.CallerChange, error) {
	if c == nil || c.Store == nil {
		return config.CallerChange{}, fmt.Errorf("add caller: no metadata store")
	}
	id := strings.TrimSpace(spec.ID)
	if err := config.ValidCallerID(id); err != nil {
		return config.CallerChange{}, config.CallerInputErrorf("%v", err)
	}
	name := strings.TrimSpace(spec.Name)
	if err := config.ValidCallerName(name); err != nil {
		return config.CallerChange{}, config.CallerInputErrorf("%v", err)
	}
	lim := config.CallerLimitsOf(spec.MaxConcurrentJobs, spec.RateLimit, spec.RateBurst)
	if err := config.ValidateCallerLimits(lim); err != nil {
		return config.CallerChange{}, err
	}
	ceilingYAML, _, err := callerCeilingForWrite(spec.Ceiling, spec.NoCeiling)
	if err != nil {
		return config.CallerChange{}, err
	}
	limitsJSON, err := json.Marshal(lim)
	if err != nil {
		return config.CallerChange{}, fmt.Errorf("encode the caller quota: %w", err)
	}
	token, hash, hint, err := mintCallerToken()
	if err != nil {
		return config.CallerChange{}, err
	}
	snap, err := c.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		taken, err := callerIDTaken(tx, next, id)
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%w: caller id %q is used by a caller, a worker or a previous caller", config.ErrCallerIDTaken, id)
		}
		now := tx.Now()
		row := jobstore.StoredCaller{
			ID: id, Name: name, TokenHash: hash, TokenHint: hint, LimitsJSON: string(limitsJSON),
			CeilingYAML: ceilingYAML, CreatedBy: actor, CreatedAt: now, UpdatedAt: now,
		}
		if err := tx.InsertCallerTx(row); err != nil {
			return err
		}
		return tx.InsertConfigAudit(jobstore.ConfigAuditEntry{
			At: now, Actor: actor, Path: callerAuditPath + id,
			AfterMasked: callerAuditValue(row.Name, row.TokenHint, row.CeilingYAML, row.LimitsJSON, false),
		})
	})
	if err != nil {
		return config.CallerChange{}, err
	}
	return config.CallerChange{Token: token, Caller: c.callerView(snap.Cfg, id)}, nil
}

// RotateCaller replaces a stored caller's token and answers the new one once. Rotation is
// allowed on a disabled or taken-over row and does not change either state: it only makes the
// previous token useless (design §「用户生命周期」).
func (c *Core) RotateCaller(id, actor string) (config.CallerChange, error) {
	if c == nil || c.Store == nil {
		return config.CallerChange{}, fmt.Errorf("rotate caller: no metadata store")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return config.CallerChange{}, config.CallerInputErrorf("caller id is required")
	}
	token, hash, hint, err := mintCallerToken()
	if err != nil {
		return config.CallerChange{}, err
	}
	snap, err := c.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		row, err := c.callerRowForWrite(tx, next, id)
		if err != nil {
			return err
		}
		now := tx.Now()
		before := callerRowAuditValue(row)
		row.TokenHash, row.TokenHint = hash, hint
		// D10: the rotation must be strictly NEWER than a takeover, because enabling a
		// taken-over row checks exactly that. The stamps are second-granular, so a rotation in
		// the same second as the takeover takes the next one.
		row.RotatedAt = now
		if row.SupersededAt != 0 && row.RotatedAt <= row.SupersededAt {
			row.RotatedAt = row.SupersededAt + 1
		}
		row.UpdatedAt = now
		if err := tx.UpdateCallerTx(row); err != nil {
			return err
		}
		return tx.InsertConfigAudit(jobstore.ConfigAuditEntry{
			At: now, Actor: actor, Path: callerAuditPath + id,
			BeforeMasked: before, AfterMasked: callerRowAuditValue(row),
		})
	})
	if err != nil {
		return config.CallerChange{}, err
	}
	return config.CallerChange{Caller: c.callerView(snap.Cfg, id), Token: token}, nil
}

// DisableCaller switches a stored caller off: it authenticates nothing from the next request on,
// every permission resolution of it is empty, and no new job of its owner starts (the ceiling of
// the entry keeps bounding the jobs it already started). Idempotent.
func (c *Core) DisableCaller(id, actor string) (config.CallerChange, error) {
	snap, err := c.writeStoredCaller(id, actor, func(row *jobstore.StoredCaller, now int64) (bool, error) {
		if row.DisabledAt != 0 {
			return false, nil // already off: nothing to record
		}
		row.DisabledAt, row.UpdatedAt = now, now
		return true, nil
	})
	if err != nil {
		return config.CallerChange{}, err
	}
	return config.CallerChange{Caller: c.callerView(snap.Cfg, strings.TrimSpace(id))}, nil
}

// EnableCaller brings a stored caller back. Two facts are checked before the row changes,
// because enabling the wrong one would silently restore an identity:
//
//   - the id must not be claimed by a configuration-file caller or a worker binding — the merge
//     would hide the row again, so the operator would believe the caller is back while it is not
//     (the add path refuses the same collisions);
//   - a row a file entry took over must have been ROTATED after the takeover (D10): otherwise
//     the token from before the file declared the id would work again.
func (c *Core) EnableCaller(id, actor string) (config.CallerChange, error) {
	snap, err := c.writeStoredCaller(id, actor, func(row *jobstore.StoredCaller, now int64) (bool, error) {
		if row.SupersededAt != 0 && row.RotatedAt <= row.SupersededAt {
			return false, fmt.Errorf("%w: caller %q was taken over by the configuration file at %d and its token was last rotated at %d",
				config.ErrCallerRotationRequired, row.ID, row.SupersededAt, row.RotatedAt)
		}
		row.DisabledAt, row.SupersededAt, row.UpdatedAt = 0, 0, now
		return true, nil
	})
	if err != nil {
		return config.CallerChange{}, err
	}
	return config.CallerChange{Caller: c.callerView(snap.Cfg, strings.TrimSpace(id))}, nil
}

// writeStoredCaller runs one state change of a stored caller plus its audit line in ONE caller
// transaction. mutate reports whether it changed anything (repeating a state change writes
// nothing). The checks every state change shares happen first: the row exists and is not a file
// caller, the id is free, a taken-over row was rotated, and its hash is usable.
func (c *Core) writeStoredCaller(id, actor string,
	mutate func(row *jobstore.StoredCaller, now int64) (bool, error)) (*ConfigSnapshot, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("update caller: no metadata store")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, config.CallerInputErrorf("caller id is required")
	}
	return c.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		row, err := c.callerRowForWrite(tx, next, id)
		if err != nil {
			return err
		}
		if err := callerStateChangeAllowed(next, row); err != nil {
			return err
		}
		now := tx.Now()
		before := callerRowAuditValue(row)
		changed, err := mutate(&row, now)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		if err := tx.UpdateCallerTx(row); err != nil {
			return err
		}
		return tx.InsertConfigAudit(jobstore.ConfigAuditEntry{
			At: now, Actor: actor, Path: callerAuditPath + id,
			BeforeMasked: before, AfterMasked: callerRowAuditValue(row),
		})
	})
}

// callerStateChangeAllowed holds a disable / enable to the two rules a state change must not
// break: the id must not be reclaimed by a file caller or a worker binding (a row the merge
// would hide again is not an enabled caller — enabling it has to be refused, not silently
// hidden), and the row's stored hash must be usable (a damaged hash needs a rotation first).
func callerStateChangeAllowed(cfg *config.Config, row jobstore.StoredCaller) error {
	if fileCallerIndex(cfg, row.ID) >= 0 {
		return fmt.Errorf("%w: caller %q is declared in the configuration file", config.ErrCallerIDTaken, row.ID)
	}
	if cfg != nil {
		if _, ok := cfg.Server.Workers[row.ID]; ok {
			return fmt.Errorf("%w: worker %q already uses the id", config.ErrCallerIDTaken, row.ID)
		}
	}
	if err := config.ValidCallerTokenHash(row.TokenHash); err != nil {
		return fmt.Errorf("%w: %v", config.ErrCallerRotationRequired, err)
	}
	return nil
}

// SetCallerCeiling replaces (or removes, via noCeiling) a caller's upper bound. A stored caller
// is written in its row, a file caller through the config write transaction (D12); both are
// validated by the same rules before anything is written (D16).
func (c *Core) SetCallerCeiling(id string, ceil *config.CallerCeiling, noCeiling bool, actor string) (config.CallerChange, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return config.CallerChange{}, config.CallerInputErrorf("caller id is required")
	}
	ceilingYAML, canonical, err := callerCeilingForWrite(ceil, noCeiling)
	if err != nil {
		return config.CallerChange{}, err
	}
	if c.callerIsFileSource(id) {
		return c.writeFileCaller(id, actor, func(cc *config.CallerConfig) { cc.Ceiling = canonical })
	}
	if c == nil || c.Store == nil {
		return config.CallerChange{}, fmt.Errorf("set caller ceiling: no metadata store")
	}
	snap, err := c.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		return c.updateStoredCallerFields(tx, next, id, actor, func(row *jobstore.StoredCaller, now int64) {
			row.CeilingYAML, row.UpdatedAt = ceilingYAML, now
		})
	})
	if err != nil {
		return config.CallerChange{}, err
	}
	return config.CallerChange{Caller: c.callerView(snap.Cfg, id)}, nil
}

// SetCallerLimits replaces a caller's quota overrides (concurrency / rate / burst). Same two
// paths as SetCallerCeiling; a zero field means "take the server.governance default".
func (c *Core) SetCallerLimits(id string, lim config.CallerLimits, actor string) (config.CallerChange, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return config.CallerChange{}, config.CallerInputErrorf("caller id is required")
	}
	if err := config.ValidateCallerLimits(lim); err != nil {
		return config.CallerChange{}, err
	}
	raw, err := json.Marshal(lim)
	if err != nil {
		return config.CallerChange{}, fmt.Errorf("encode the caller quota: %w", err)
	}
	if c.callerIsFileSource(id) {
		return c.writeFileCaller(id, actor, func(cc *config.CallerConfig) {
			cc.MaxConcurrentJobs, cc.RateLimit, cc.RateBurst = lim.MaxConcurrentJobs, lim.RateLimit, lim.RateBurst
		})
	}
	if c == nil || c.Store == nil {
		return config.CallerChange{}, fmt.Errorf("set caller limits: no metadata store")
	}
	snap, err := c.UpdateCallers(func(next *config.Config, tx jobstore.CallerTx) error {
		return c.updateStoredCallerFields(tx, next, id, actor, func(row *jobstore.StoredCaller, now int64) {
			row.LimitsJSON, row.UpdatedAt = string(raw), now
		})
	})
	if err != nil {
		return config.CallerChange{}, err
	}
	return config.CallerChange{Caller: c.callerView(snap.Cfg, id)}, nil
}

// updateStoredCallerFields applies one field change to a stored caller row and writes its audit
// line. The id must name a stored row: a file caller answers ErrCallerFileSource.
func (c *Core) updateStoredCallerFields(tx jobstore.CallerTx, next *config.Config, id, actor string,
	apply func(row *jobstore.StoredCaller, now int64)) error {
	row, err := c.callerRowForWrite(tx, next, id)
	if err != nil {
		return err
	}
	now := tx.Now()
	before := callerRowAuditValue(row)
	apply(&row, now)
	if err := tx.UpdateCallerTx(row); err != nil {
		return err
	}
	return tx.InsertConfigAudit(jobstore.ConfigAuditEntry{
		At: now, Actor: actor, Path: callerAuditPath + id,
		BeforeMasked: before, AfterMasked: callerRowAuditValue(row),
	})
}

// writeFileCaller edits a caller declared in the configuration FILE through the ordinary config
// write transaction: clone → mutate → validate → save → reload. A ceiling or quota is the one
// thing this API may change on such an entry (D12); the audit line is written AFTER the file,
// because the two cannot share a transaction — a failed audit is logged and reported in the
// answer's warnings, and the change stands (it is already on disk).
func (c *Core) writeFileCaller(id, actor string, apply func(cc *config.CallerConfig)) (config.CallerChange, error) {
	if c == nil {
		return config.CallerChange{}, fmt.Errorf("update caller: no core")
	}
	before := ""
	err := c.Update(func(next *config.Config) error {
		idx := fileCallerIndex(next, id)
		if idx < 0 {
			return fmt.Errorf("%w: %s", config.ErrCallerNotFound, id)
		}
		before = callerEntryAuditValue(next.Server.Callers[idx])
		apply(&next.Server.Callers[idx])
		return c.validateCallerCandidate(next)
	})
	if err != nil {
		return config.CallerChange{}, err
	}
	cfg := c.Config()
	var warnings []string
	idx := fileCallerIndex(cfg, id)
	after := ""
	if idx >= 0 {
		after = callerEntryAuditValue(cfg.Server.Callers[idx])
	}
	if c.Store != nil {
		if aerr := c.Store.InsertConfigAudit(jobstore.ConfigAuditEntry{
			Actor: actor, Path: callerAuditPath + id, BeforeMasked: before, AfterMasked: after,
		}); aerr != nil {
			slog.Error("caller: the audit line was not written", "caller", id, "error", aerr)
			warnings = append(warnings,
				"the change was written to the configuration file but its audit line was not: "+aerr.Error())
		}
	}
	change := config.CallerChange{Caller: c.callerView(cfg, id)}
	change.Warnings = append(change.Warnings, warnings...)
	return change, nil
}

// callerRowForWrite classifies the id of a write that changes a caller's STATE: a stored row is
// the only thing it may touch, so a file caller is a 409 (its token lives in the file, and a
// rotation would have to write it there) and an unknown id is a 404.
func (c *Core) callerRowForWrite(tx jobstore.CallerTx, cfg *config.Config, id string) (jobstore.StoredCaller, error) {
	row, ok, err := tx.GetCaller(id)
	if err != nil {
		return jobstore.StoredCaller{}, err
	}
	if ok {
		return row, nil
	}
	if fileCallerIndex(cfg, id) >= 0 {
		return jobstore.StoredCaller{}, fmt.Errorf(
			"%w: caller %q can only be changed in the configuration file (rotate / disable / enable are for callers created in the console or the CLI)",
			config.ErrCallerFileSource, id)
	}
	return jobstore.StoredCaller{}, fmt.Errorf("%w: %s", config.ErrCallerNotFound, id)
}

// callerIsFileSource reports whether the LIVE configuration declares id in the file. A stored
// caller is never answered true, even though it sits in the same list.
func (c *Core) callerIsFileSource(id string) bool {
	if c == nil {
		return false
	}
	return fileCallerIndex(c.Config(), id) >= 0
}

// fileCallerIndex returns the position of the FILE caller id in the server caller list, or -1.
func fileCallerIndex(cfg *config.Config, id string) int {
	if cfg == nil || id == "" {
		return -1
	}
	for i := range cfg.Server.Callers {
		if cfg.Server.Callers[i].IsStoredCaller() {
			continue
		}
		if cfg.Server.Callers[i].ID == id {
			return i
		}
	}
	return -1
}

// callerIDTaken reports whether id belongs to another identity: a stored row, a file caller or a
// worker binding. Ids are never reused, so the add path asks about every one of them.
func callerIDTaken(tx jobstore.CallerTx, cfg *config.Config, id string) (bool, error) {
	if _, ok, err := tx.GetCaller(id); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	if fileCallerIndex(cfg, id) >= 0 {
		return true, nil
	}
	if cfg != nil {
		if _, ok := cfg.Server.Workers[id]; ok {
			return true, nil
		}
	}
	return false, nil
}

// callerCeilingForWrite turns the two forms of the request into the stored form: a ceiling, or
// the explicit "no ceiling" (D16). Exactly one of them must be given.
func callerCeilingForWrite(ceil *config.CallerCeiling, noCeiling bool) (string, *config.CallerCeiling, error) {
	if noCeiling {
		if ceil != nil {
			return "", nil, config.CallerInputErrorf("either list a ceiling or say no_ceiling, not both")
		}
		return "", nil, nil
	}
	if ceil == nil {
		return "", nil, config.CallerInputErrorf(
			"a ceiling is required: list what the caller may do, or say no_ceiling to leave it unlimited")
	}
	return config.CanonicalCallerCeiling(ceil)
}

// ---- the read side ----

// ListCallers reports every identity of this deployment in the management view: the
// configuration-file callers first (the operator's own, in file order), then the stored rows in
// creation order, including the disabled and the taken-over ones (an operator has to see what
// was taken away, and enable a disabled caller again).
func (c *Core) ListCallers() ([]config.CallerView, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("list callers: no metadata store")
	}
	rows, err := c.Store.ListStoredCallers()
	if err != nil {
		return nil, fmt.Errorf("read stored callers: %w", err)
	}
	cfg := c.Config()
	out := make([]config.CallerView, 0, len(cfg.Server.Callers)+len(rows))
	for _, cc := range cfg.Server.Callers {
		if cc.IsStoredCaller() {
			continue
		}
		out = append(out, c.fileCallerView(cfg, cc))
	}
	live := make(map[string]config.CallerConfig, len(cfg.Server.Callers))
	for _, cc := range cfg.Server.Callers {
		if cc.ID != "" {
			live[cc.ID] = cc
		}
	}
	for _, row := range rows {
		entry, ok := live[row.ID]
		out = append(out, c.storedCallerView(cfg, row, entry, ok))
	}
	return out, nil
}

// ListCallerAudit reports the config_audit lines of every caller change, newest first.
func (c *Core) ListCallerAudit(limit int) ([]jobstore.ConfigAuditEntry, error) {
	if c == nil || c.Store == nil {
		return nil, fmt.Errorf("list caller audit: no metadata store")
	}
	entries, err := c.Store.ListConfigAudit(callerAuditPath, limit)
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []jobstore.ConfigAuditEntry{}
	}
	return entries, nil
}

// callerView is the management view of one id after a write: the live snapshot says whether it
// is in force, the row carries the stamps.
func (c *Core) callerView(cfg *config.Config, id string) config.CallerView {
	if cfg != nil {
		for _, cc := range cfg.Server.Callers {
			if cc.ID != id {
				continue
			}
			if !cc.IsStoredCaller() {
				return c.fileCallerView(cfg, cc)
			}
			row, _, err := c.Store.GetStoredCaller(id)
			if err != nil {
				row = jobstore.StoredCaller{ID: cc.ID}
			}
			return c.storedCallerView(cfg, row, cc, true)
		}
	}
	// Not in the snapshot: a disabled or taken-over row (it must not be part of it) still has
	// to appear in a list, and an id that vanished entirely answers an empty row.
	if c.Store != nil {
		if row, ok, err := c.Store.GetStoredCaller(id); err == nil && ok {
			return c.storedCallerView(cfg, row, config.CallerConfig{}, false)
		}
	}
	return config.CallerView{ID: id}
}

// fileCallerView renders a caller declared in the configuration file.
func (c *Core) fileCallerView(cfg *config.Config, cc config.CallerConfig) config.CallerView {
	out := config.CallerView{
		ID: cc.ID, Name: cc.Name, Source: config.CallerSourceFile, State: config.CallerStateActive,
		TokenSet: cc.Token != "" || cc.TokenEnv != "", TokenHint: cc.TokenHint,
		MaxConcurrentJobs: cc.MaxConcurrentJobs, RateLimit: cc.RateLimit, RateBurst: cc.RateBurst,
		Ceiling: cc.Ceiling,
	}
	out.Warnings = callerWarnings(cfg, out)
	return out
}

// storedCallerView renders one auth_callers row. The live entry (when the merge kept it) is the
// authority on what applies; the row adds the stamps and the state the snapshot cannot show (a
// disabled or taken-over row is still listed).
func (c *Core) storedCallerView(cfg *config.Config, row jobstore.StoredCaller,
	entry config.CallerConfig, live bool) config.CallerView {
	out := config.CallerView{
		ID: row.ID, Name: row.Name, Source: config.CallerSourceDB, State: config.CallerStateActive,
		TokenSet: true, TokenHint: row.TokenHint,
		CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, RotatedAt: row.RotatedAt, UpdatedAt: row.UpdatedAt,
	}
	switch {
	case row.SupersededAt != 0:
		out.State = config.CallerStateSuperseded
	case row.DisabledAt != 0:
		out.State = config.CallerStateDisabled
	}
	if lim, err := config.DecodeCallerLimits(row.LimitsJSON); err == nil {
		out.MaxConcurrentJobs, out.RateLimit, out.RateBurst = lim.MaxConcurrentJobs, lim.RateLimit, lim.RateBurst
	}
	if strings.TrimSpace(row.CeilingYAML) != "" {
		if ceil, err := config.DecodeCallerCeiling(row.CeilingYAML); err == nil {
			out.Ceiling = ceil
		} else {
			out.Warnings = append(out.Warnings,
				"the stored ceiling cannot be decoded ("+err.Error()+"): run `gofer auth caller repair "+row.ID+" --offline`")
		}
	}
	if live {
		// What the merge applied wins (a cleared duplicate name, the effective values).
		out.Name = entry.Name
		out.MaxConcurrentJobs, out.RateLimit, out.RateBurst = entry.MaxConcurrentJobs, entry.RateLimit, entry.RateBurst
		out.Ceiling = entry.Ceiling
	}
	out.Warnings = append(out.Warnings, callerWarnings(cfg, out)...)
	return out
}

// callerWarnings reports the notices one caller's limits produce when it is written or listed,
// so the console and the CLI can show them instead of an operator finding out later:
//
//   - a ceiling that admits the machine runner is not a security boundary (the load reports the
//     same through LoadWarnings);
//   - a caller with NO ceiling may edit the configuration through the ordinary write permission
//     while governance.require_admin_capability is off (design「剩余风险」: no new limit, but the
//     operator should know what an unlimited token can do).
func callerWarnings(cfg *config.Config, view config.CallerView) []string {
	var out []string
	if view.Ceiling != nil {
		if w, ok := config.CallerCeilingWarning("caller", config.CallerConfig{ID: view.ID, Ceiling: view.Ceiling}); ok {
			out = append(out, w)
		}
	}
	if view.Ceiling == nil && cfg != nil && !cfg.Server.Governance.RequireAdminCapability {
		out = append(out, fmt.Sprintf(
			"caller %q has no ceiling: while governance.require_admin_capability is off, this caller "+
				"holds `config.write` and can set `agents.*.command`", view.ID))
	}
	return out
}

// callerAuditValue renders the audited picture of one caller state. An audit line (and the log,
// and a terminal) may carry these fields and nothing else: never the token, never its hash
// (SR1301).
func callerAuditValue(name, hint, ceilingYAML, limitsJSON string, disabled bool) string {
	return fmt.Sprintf("name=%q hint=%q ceiling_yaml=%q limits_json=%q disabled=%t",
		name, hint, ceilingYAML, limitsJSON, disabled)
}

// callerRowAuditValue is callerAuditValue for a stored row.
func callerRowAuditValue(row jobstore.StoredCaller) string {
	return callerAuditValue(row.Name, row.TokenHint, row.CeilingYAML, row.LimitsJSON, row.DisabledAt != 0)
}

// callerEntryAuditValue is callerAuditValue for a caller of the configuration file.
func callerEntryAuditValue(cc config.CallerConfig) string {
	ceil := ""
	if cc.Ceiling != nil {
		if b, err := yaml.Marshal(cc.Ceiling); err == nil {
			ceil = strings.TrimSpace(string(b))
		}
	}
	lim := ""
	if cc.MaxConcurrentJobs != 0 || cc.RateLimit != 0 || cc.RateBurst != 0 {
		if b, err := json.Marshal(config.CallerLimitsOf(cc.MaxConcurrentJobs, cc.RateLimit, cc.RateBurst)); err == nil {
			lim = string(b)
		}
	}
	return callerAuditValue(cc.Name, cc.TokenHint, ceil, lim, false)
}

// validateCallerCandidate is the validation the caller write path applies to a candidate
// configuration: the loader's own checks plus the agent-mode checks the HTTP write path
// also runs. Core does not import httpapi, so this is the one place both are combined for
// this path.
func (c *Core) validateCallerCandidate(cfg *config.Config) error {
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	if err := agent.ValidateConfig(cfg); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}
	return nil
}
