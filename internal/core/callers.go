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
	"errors"
	"fmt"

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
		if _, err := config.ApplyStoredCallers(next, storedCallerInputs(rows)); err != nil {
			return err
		}
		return c.validateCallerCandidate(next)
	})
	if err != nil {
		return nil, err
	}
	return c.reloadLocked(next), nil
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
