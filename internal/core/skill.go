package core

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/skill"
	"github.com/inhere/gofer/internal/xfer"
)

// skill.go is JOB-10's hub side of the skill-library seam: the ONE place that knows
// both the job package's contract (job.SkillLibrary) and the skill store + transfer
// manager, exactly like job_xfer.go does for the file seam (G022 — neither package
// imports the other).
//
//   - Get answers the metadata the prompt list needs (name, description, files).
//   - Mount copies a skill into a LOCAL job's result dir.
//   - Stage puts a skill's files into the transfer staging area as ordinary puts
//     targeting the job's worker, so a dispatched job's files ride the SAME channel
//     XFER-01 built (Base=result_dir keeps them out of the working tree).
type hubSkillLibrary struct {
	store *skill.Store
	mgr   *xfer.Manager
}

// Skills returns the JOB-10 skill library store (nil when the library was never
// built). It is the SAME store the job seam above resolves bindings against and the
// one serve injects into httpapi (SetSkills), so the CLI/HTTP surface and the dispatch
// path can never disagree about what a skill contains.
//
// Nil is a "not wired" answer, not a panic: an HTTP surface without it answers 503
// (the same degradation /v1/xfer uses), which is what a caller built before Build
// wired the library must see.
func (c *Core) Skills() *skill.Store {
	if c == nil || c.skillLib == nil {
		return nil
	}
	return c.skillLib.store
}

// buildSkillLibrary opens the library at <config-dir>/skills (the same directory the
// imports land in, next to the rest of the operator's config) over the metadata
// store's skills table, with the byte caps from server.skill_limits.
func buildSkillLibrary(cfg *config.Config, repo *jobstore.Store, mgr *xfer.Manager) (*hubSkillLibrary, error) {
	lim := skill.DefaultLimits()
	if v := cfg.Server.SkillLimits.MaxFileBytes; v > 0 {
		lim.MaxFileBytes = v
	}
	if v := cfg.Server.SkillLimits.MaxTotalBytes; v > 0 {
		lim.MaxTotalBytes = v
	}
	cfgDir, err := config.ConfigDir()
	if err != nil {
		return nil, fmt.Errorf("resolve config dir: %w", err)
	}
	st, err := skill.NewStore(filepath.Join(cfgDir, "skills"), repo, lim)
	if err != nil {
		return nil, err
	}
	return &hubSkillLibrary{store: st, mgr: mgr}, nil
}

// Get implements job.SkillLibrary. A read error is reported as "not present": the
// caller must not mount a library it cannot read, and a submit naming that skill is
// then rejected instead of running without its rules.
func (h hubSkillLibrary) Get(name string) (job.SkillInfo, bool) {
	s, ok, err := h.store.Get(name)
	if err != nil || !ok {
		return job.SkillInfo{}, false
	}
	info := job.SkillInfo{Name: s.Name, Description: s.Description, Size: s.Size}
	for _, f := range s.Files {
		info.Files = append(info.Files, job.SkillFile{Path: f.Path, Size: f.Size})
	}
	return info, true
}

// Mount implements job.SkillLibrary for a local job. dstDir is the job's `skills/`
// ROOT (the seam's contract), while skill.Store.Mount copies a skill's tree into the
// directory it is given — so the skill's own directory is added HERE. That is the
// layout the job side renders into the prompt manifest and the worker side derives
// its upload destinations from (job.SkillDest), and it is the reason two skills
// mounted into one job cannot overwrite each other.
func (h hubSkillLibrary) Mount(name, dstDir string) (int64, error) {
	return h.store.Mount(name, filepath.Join(dstDir, name))
}

// Stage implements job.SkillLibrary for a worker-bound job: one staged put per file,
// each with the result-dir base. The destination is derived by job.SkillDest so the
// staging side and the mounting side cannot drift on the path.
func (h hubSkillLibrary) Stage(_ context.Context, name, runner, projectKey, caller string) ([]job.UploadSpec, error) {
	s, ok, err := h.store.Get(name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("%w: skill %q", skill.ErrNotFound, name)
	}
	out := make([]job.UploadSpec, 0, len(s.Files))
	for _, f := range s.Files {
		src := filepath.Join(h.store.Dir(name), filepath.FromSlash(f.Path))
		dest := job.SkillDest(name, f.Path)
		rec, err := h.mgr.StagePutFromFile(caller, runner, projectKey, src, dest)
		if err != nil {
			return nil, fmt.Errorf("stage skill %s/%s: %w", name, f.Path, err)
		}
		out = append(out, job.UploadSpec{XferID: rec.ID, Dest: dest, Base: job.SkillBaseResultDir})
	}
	return out, nil
}
