package job

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
)

// skills.go is JOB-10's job-side half: a skill is "working-method knowledge" the
// server owns, bound by a UNION of four levels (design §一.3) and materialized into
// the job's OWN result dir at dispatch time — never into the project working tree,
// which concurrent jobs share and git watches.
//
//   - Resolution happens ONCE, in Submit, from the same config snapshot as every
//     other admitted value, and is written back onto the request: request_json, the
//     persisted row, the Forward and the executing machine all carry ONE decided
//     list, and a rerun repeats it.
//   - Materialization is per executing machine: the hub copies the library into a
//     local job's result dir (SkillLibrary.Mount); a WORKER job's files travel as
//     ordinary staged uploads with Base=result_dir (the same channel XFER-01 uses),
//     so the machine that owns the directory writes it.
//   - The prompt gets a LIST (name + description + the SKILL.md path as the
//     executing machine sees it), not the contents: the agent decides what to read.
//
// The skill library itself lives behind SkillLibrary — this package never imports
// internal/skill or internal/xfer (G022).

// SkillFile is one file of a skill, relative to the skill dir (slash-separated).
type SkillFile struct {
	Path string `json:"path"`
	Size int64  `json:"size,omitempty"`
}

// SkillInfo is what the job package needs to know about one library entry: what to
// put in the prompt list and which files to carry to a worker.
type SkillInfo struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Files       []SkillFile `json:"files,omitempty"`
	Size        int64       `json:"size,omitempty"`
}

// SkillLibrary is the server's skill asset library as this package needs it.
// *skill.Store-backed adapter is wired at assemble time (core.Build for a hub, the
// worker command for a worker).
type SkillLibrary interface {
	// Get returns one entry's metadata; ok=false means the name is unknown (a submit
	// naming it is rejected rather than run without its rules).
	Get(name string) (SkillInfo, bool)
	// Mount copies the whole skill under dstDir (the result dir's `skills/`) and
	// returns the bytes written. It is how a LOCAL job gets its files.
	Mount(name, dstDir string) (int64, error)
	// Stage turns each of the skill's files into a staged put transfer targeting
	// runner and returns one UploadSpec per file, all with Base=result_dir — the
	// worker fetches the bytes over its own authenticated HTTP session. It is how a
	// WORKER job gets its files.
	Stage(ctx context.Context, name, runner, projectKey, caller string) ([]UploadSpec, error)
}

// SetSkillLibrary installs the skill-library seam. Without one, a job that binds a
// skill fails validation with an explanation instead of running without it.
func (s *Service) SetSkillLibrary(l SkillLibrary) { s.skills = l }

const (
	// skillsDirName is the mount directory inside the job's result dir:
	// <result_dir>/skills/<name>/…. It is job-private, so two concurrent jobs never
	// fight over it and a job that "edits" a skill changes only its own copy.
	skillsDirName = "skills"
	// SkillBaseResultDir is the UploadSpec.Base value that places a file relative to
	// the job's RESULT dir instead of its cwd (design §一.4). A worker below
	// wsproto.SkillsMinProtocolVersion would ignore it and write into the working
	// tree, so the hub must never send one to such a worker.
	SkillBaseResultDir = "result_dir"
	// skillsPromptHeader opens the generated list. The manifest sits at the TOP of
	// the prompt — the agent reads what it may read before it reads what to do.
	skillsPromptHeader = "## 可用技能（gofer 挂载，按需阅读）"
	// skillsPromptFooter is the one instruction that makes the list useful.
	skillsPromptFooter = "先读与本任务相关的 SKILL.md，再动手。"
	// skillsDirPlaceholder is what a REMOTE job's manifest carries instead of an
	// absolute path: the submitting hub cannot know the worker's result dir, so the
	// executing machine substitutes it (see substituteSkillsDir). A local job's
	// manifest already holds the real path, so the substitution is a no-op there.
	skillsDirPlaceholder = "{{skills_dir}}"
	// goferSkillsDirEnv is the env var that lets an agent (or a wrapper script) find
	// the mount without parsing the prompt.
	goferSkillsDirEnv = "GOFER_SKILLS_DIR"
)

// SkillDest is the result-dir-relative destination of one skill file, e.g.
// skills/house-rules/SKILL.md. It is exported because the STAGING side (which lives
// outside this package, next to the transfer manager) must derive the same path the
// mounting side uses — one definition, no drift.
func SkillDest(name, rel string) string {
	return skillsDirName + "/" + name + "/" + filepath.ToSlash(rel)
}

// skillDestFor is the in-package spelling used by this file and its tests.
func skillDestFor(name, rel string) string { return SkillDest(name, rel) }

// substituteSkillsDir replaces the placeholder with THIS machine's mount path. It is
// idempotent and safe to run for every job: a manifest rendered locally already has
// the real path and contains no placeholder.
func substituteSkillsDir(prompt, resultDir string) string {
	if !strings.Contains(prompt, skillsDirPlaceholder) {
		return prompt
	}
	return strings.ReplaceAll(prompt, skillsDirPlaceholder, filepath.Join(resultDir, skillsDirName))
}

// resolveSkills expands the four binding levels into the job's final skill list,
// IN PLACE on the request: the union (config.EffectiveSkills), the name check against
// the library, and — for a job that will run on a WORKER — the staged upload specs
// that carry the files there.
//
// It runs in Submit BEFORE validate/validate's side effects, so an unknown skill name
// is a rejected submit (nothing is created, nothing runs) that names the skill.
func (s *Service) resolveSkills(cfg *config.Config, req *JobRequest, remote bool) error {
	// A dispatched or forwarded job carries the SUBMITTING machine's decision
	// (SkillsResolved): re-deriving the union here would add the executing machine's
	// own config bindings on top and mount a different set than the hub decided.
	if req.SkillsResolved {
		return s.stageSkills(req, remote)
	}

	agentType := ""
	if ac, ok := agent.ResolveAgent(cfg, req.Agent); ok {
		agentType = ac.Type
	}
	req.Skills = cfg.EffectiveSkills(req.ProjectKey, req.Agent, req.Skills, req.NoSkills, agentType)
	if len(req.Skills) == 0 {
		return nil
	}
	if s.skills == nil {
		return fmt.Errorf("%w: skills are not available on this server", ErrInvalidRequest)
	}
	for _, name := range req.Skills {
		if _, ok := s.skills.Get(name); !ok {
			return fmt.Errorf("%w: unknown skill %q", ErrInvalidRequest, name)
		}
	}
	return s.stageSkills(req, remote && isWorkerRunner(cfg, req.Runner))
}

// stageSkills turns a WORKER-bound job's skills into staged uploads. A local job
// needs nothing here: its files are copied straight into its result dir at execution
// time (mountSkills). A peer-http job gets no files (its transport carries none) and
// no prompt list either — see the manifest step in Submit — because a path the
// executing machine cannot read is worse than no path at all.
func (s *Service) stageSkills(req *JobRequest, stage bool) error {
	if len(req.Skills) == 0 {
		return nil
	}
	if !stage {
		return nil
	}
	if s.skills == nil {
		return fmt.Errorf("%w: skills are not available on this server", ErrInvalidRequest)
	}
	ctx := context.Background()
	for _, name := range req.Skills {
		ups, err := s.skills.Stage(ctx, name, req.Runner, req.ProjectKey, req.CallerID)
		if err != nil {
			return fmt.Errorf("%w: stage skill %q: %s", ErrInvalidRequest, name, err.Error())
		}
		req.Uploads = append(req.Uploads, ups...)
	}
	return nil
}

// skillsManifest renders the prompt list for the resolved skills. dir is the mount
// root (or skillsDirPlaceholder for a job whose mount path belongs to another
// machine); each entry names the skill, what it is for, and where its SKILL.md is.
func skillsManifest(lib SkillLibrary, names []string, dir string) string {
	if len(names) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(skillsPromptHeader)
	b.WriteString("\n")
	for _, name := range names {
		info, ok := lib.Get(name)
		desc := ""
		if ok {
			desc = strings.TrimSpace(info.Description)
		}
		b.WriteString("- ")
		b.WriteString(name)
		if desc != "" {
			b.WriteString("：")
			b.WriteString(desc)
		}
		b.WriteString(" → ")
		b.WriteString(filepath.Join(dir, name, "SKILL.md"))
		b.WriteString("\n")
	}
	b.WriteString(skillsPromptFooter)
	b.WriteString("\n\n")
	return b.String()
}

// skillsPromptPrefix is the manifest text prepended to the prompt at submit, or ""
// when the job carries no skills.
func (s *Service) skillsPromptPrefix(req *JobRequest, resultDir string, remote bool) string {
	if s.skills == nil || len(req.Skills) == 0 {
		return ""
	}
	dir := filepath.Join(resultDir, skillsDirName)
	if remote {
		dir = skillsDirPlaceholder
	}
	return skillsManifest(s.skills, req.Skills, dir)
}

// mountSkills places the job's skills in ITS OWN result dir on THIS machine, before
// the agent starts: the local half of the design's materialization rule. A failure
// fails the job — an agent told to read a skill that is not there would guess.
//
// A dispatched worker job does NOT come here: its files arrive as uploads with
// Base=result_dir (materializeUploads), and its own library may not hold the skill at
// all. So this runs only for the machine that RESOLVED the binding.
func (s *Service) mountSkills(entry *jobEntry, req runner.Request) error {
	if s.skills == nil || len(req.Skills) == 0 || req.SkillsResolved {
		return nil
	}
	entry.mu.Lock()
	resultDir := entry.result.ResultDir
	entry.mu.Unlock()
	dst := filepath.Join(resultDir, skillsDirName)
	var total int64
	var mounted []string
	for _, name := range req.Skills {
		n, err := s.skills.Mount(name, dst)
		if err != nil {
			return fmt.Errorf("mount skill %s: %w", name, err)
		}
		total += n
		mounted = append(mounted, name)
	}
	sort.Strings(mounted)
	s.recordEvent(req.JobID, EventJobSkillsMounted, map[string]any{
		"names": mounted, "bytes": total,
	})
	return nil
}
