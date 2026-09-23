package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/skill"
)

// TestSkillMountLayoutMatchesManifestPath pins the ONE layout JOB-10 promises: a
// local job's skill lands in its OWN subdirectory of the result dir's `skills/`
// (<result_dir>/skills/<name>/SKILL.md), which is exactly the path the executing
// machine renders into the prompt manifest (job.mountSkills → skillsPromptPrefix) and
// the path the worker side derives its upload destinations from (job.SkillDest).
//
// It exists because the two halves drifted once: skill.Store.Mount copies a skill's
// tree INTO the directory it is given, while the job-side seam passes the skills ROOT
// — so the adapter has to add the skill's own directory. Mounting the files loose in
// the root left every manifest pointing at a path that did not exist (and two skills
// overwriting each other), which no unit test could see while the job package's stub
// library joined the name itself. A real mount on a real server is what exposed it.
func TestSkillMountLayoutMatchesManifestPath(t *testing.T) {
	root := t.TempDir()
	meta, err := jobstore.Open(filepath.Join(root, "meta.db"))
	if err != nil {
		t.Fatalf("open jobstore: %v", err)
	}
	t.Cleanup(func() { _ = meta.Close() })

	st, err := skill.NewStore(filepath.Join(root, "skills"), meta, skill.DefaultLimits())
	if err != nil {
		t.Fatalf("new skill store: %v", err)
	}
	src := filepath.Join(root, "src", "house-rules")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	body := "---\nname: house-rules\ndescription: house rules\n---\n\n# rules\n"
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := st.Import(src, "test"); err != nil {
		t.Fatalf("import: %v", err)
	}

	lib := hubSkillLibrary{store: st}
	resultDir := filepath.Join(root, "job")
	// The job side hands the adapter the skills ROOT (job.skillsDirName).
	if n, err := lib.Mount("house-rules", filepath.Join(resultDir, "skills")); err != nil {
		t.Fatalf("mount: %v", err)
	} else if n == 0 {
		t.Fatal("mount wrote 0 bytes")
	}

	// The mounted path IS the manifest path: one definition, no drift.
	want := filepath.Join(resultDir, filepath.FromSlash(job.SkillDest("house-rules", "SKILL.md")))
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("read the manifest's path %s: %v", want, err)
	}
	if string(got) != body {
		t.Fatalf("mounted SKILL.md = %q, want the imported bytes", string(got))
	}
}
