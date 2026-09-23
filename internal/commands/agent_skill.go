package commands

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/skill"
)

// agentSkillCaller is the `updated_by` stamp the CLI writes on a local import or
// update. The server stamps its own caller id for HTTP calls; a direct local run has
// no caller identity to offer, so it names the surface that did the write.
const agentSkillCaller = "cli"

// agentSkillOpts holds the flags shared by the `agent skill` subcommands. The
// sub-group lives under `agent` (design §一.2, decision: no new top-level command) —
// a skill is what an agent knows, so it is found where the agents are.
var agentSkillOpts = struct {
	local bool
	out   string
}{}

// newAgentSkillCmd builds the `agent skill` group: ls/show/import/update/rm/export
// over the JOB-10 library (`<config-dir>/skills/<name>/` + its index).
//
// Dual mode, exactly like `agent list`: a client node — or any box with no local
// server config of its own, such as the worker/container this sub-group was reported
// broken on (bd h-aii-uzvc) — goes over HTTP to the server that owns the library, so
// one box can manage a fleet's skills; --local opens the library here instead —
// server-style: config → jobstore → skill.NewStore(<config-dir>/skills).
func newAgentSkillCmd() *gcli.Command {
	// bindSkillConn binds what every skill subcommand needs: the config path, the
	// connection flags (client mode), and the --local escape hatch.
	bindSkillConn := func(c *gcli.Command) {
		bindConfigFlag(c)
		bindServerFlags(c)
		c.BoolOpt(&agentSkillOpts.local, "local", "", false, "operate on the LOCAL skill library even in client mode")
	}
	return &gcli.Command{
		Name: "skill",
		Desc: "Manage the server-side skill library (list/show/import/update/rm/export)",
		Subs: []*gcli.Command{
			{
				Name:    "list",
				Desc:    "List skills with their version, size and description",
				Aliases: []string{"ls"},
				Config:  bindSkillConn,
				Func:    runAgentSkillList,
			},
			{
				Name: "show",
				Desc: "Show a skill: metadata, file list and the SKILL.md body",
				Config: func(c *gcli.Command) {
					bindSkillConn(c)
					c.AddArg("name", "skill name", true)
				},
				Func: runAgentSkillShow,
			},
			{
				Name: "import",
				Desc: "Import a skill from a directory, .zip, http(s) .zip URL or git+https://…#subdir",
				Config: func(c *gcli.Command) {
					bindSkillConn(c)
					c.AddArg("src", "source: local dir, local .zip, http(s) URL, git+https://…#subdir", true)
				},
				Func: runAgentSkillImport,
			},
			{
				Name: "update",
				Desc: "Re-fetch a skill from its recorded source and report the file-level diff",
				Config: func(c *gcli.Command) {
					bindSkillConn(c)
					c.AddArg("name", "skill name", true)
				},
				Func: runAgentSkillUpdate,
			},
			{
				Name:    "rm",
				Desc:    "Delete a skill from the library",
				Aliases: []string{"remove"},
				Config: func(c *gcli.Command) {
					bindSkillConn(c)
					c.AddArg("name", "skill name", true)
				},
				Func: runAgentSkillRemove,
			},
			{
				Name: "export",
				Desc: "Write a skill out as a .zip",
				Config: func(c *gcli.Command) {
					bindSkillConn(c)
					c.AddArg("name", "skill name", true)
					c.StrOpt(&agentSkillOpts.out, "out", "o", "", "output path (default: <name>.zip in the cwd)")
				},
				Func: runAgentSkillExport,
			},
		},
	}
}

// agentSkillRemote resolves where the skill commands read from, with the shared
// dual-mode rule (useServerAPI): a client node, or any node that has no local server
// config to host a library, goes over HTTP; --local forces the local copy.
func agentSkillRemote() serverAPIChoice {
	return useServerAPI(agentSkillOpts.local)
}

// openLocalSkillStore opens the library the way the server does: the resolved
// config, its metadata db, and <config-dir>/skills with the server's skill_limits
// (zero fields fall back to skill.DefaultLimits, which the store applies itself).
// The returned func closes the db and must always be called.
//
// A missing config REFUSES rather than degrading to a fresh empty library: without
// a config there is no config dir, no index and nothing that could have imported a
// skill — silently creating a db over one would answer "no skills" to an operator
// who is actually pointed at the wrong box. Since the dual-mode rule (useServerAPI)
// only reaches the local path when the box has a config or --local asked for it, the
// text names --local as the reason: that flag is what got us here.
func openLocalSkillStore() (*skill.Store, func(), error) {
	cfg, path, err := config.Load(config.InputCfgFile)
	if err != nil {
		return nil, nil, err
	}
	if path == "" {
		return nil, nil, fmt.Errorf(
			"no local gofer config found: --local reads the skill library beside the server's config, " +
				"and this box has none; pass -c/--config to point at one, or drop --local to use the " +
				"server's library over HTTP")
	}
	repo, err := jobstore.Open(cfg.ResolveDBPath())
	if err != nil {
		return nil, nil, err
	}
	closeStore := func() { _ = repo.Close() }

	dir, err := config.ConfigDir()
	if err != nil {
		closeStore()
		return nil, nil, fmt.Errorf("resolve config dir: %w", err)
	}
	lim := skill.DefaultLimits()
	if v := cfg.Server.SkillLimits.MaxFileBytes; v > 0 {
		lim.MaxFileBytes = v
	}
	if v := cfg.Server.SkillLimits.MaxTotalBytes; v > 0 {
		lim.MaxTotalBytes = v
	}
	st, err := skill.NewStore(filepath.Join(dir, "skills"), repo, lim)
	if err != nil {
		closeStore()
		return nil, nil, err
	}
	return st, closeStore, nil
}

// runAgentSkillList lists the library's entries, one line each.
func runAgentSkillList(c *gcli.Command, _ []string) error {
	if ch := agentSkillRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		list, err := cli.SkillList()
		if err != nil {
			return ch.wrap(err)
		}
		printSkillList(c, list)
		return nil
	}
	st, closeStore, err := openLocalSkillStore()
	if err != nil {
		return err
	}
	defer closeStore()
	list, err := st.List()
	if err != nil {
		return err
	}
	printSkillList(c, list)
	return nil
}

// printSkillList renders the ls table: the version is shortened to its first 12
// hex chars (a 64-char content hash is unreadable in a table; `show` has the rest).
func printSkillList(c *gcli.Command, list []skill.Skill) {
	if len(list) == 0 {
		c.Println("(no skills)")
		return
	}
	for _, s := range list {
		desc := s.Description
		if desc == "" {
			desc = "-"
		}
		c.Printf("%-24s %-12s %-9s %s\n", s.Name, shortSkillVersion(s.Version), humanBytes(s.Size), desc)
	}
}

// runAgentSkillShow prints one skill: metadata, its file list and the SKILL.md body
// (the text the agent actually reads).
func runAgentSkillShow(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return fmt.Errorf("agent skill show requires a <name> argument")
	}
	var view client.SkillView
	if ch := agentSkillRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		view, err = cli.SkillShow(name)
		if err != nil {
			return ch.wrap(err)
		}
	} else {
		st, closeStore, err := openLocalSkillStore()
		if err != nil {
			return err
		}
		defer closeStore()
		view, err = localSkillView(st, name)
		if err != nil {
			return err
		}
	}
	printSkillView(c, view)
	return nil
}

// localSkillView reads the index row plus the SKILL.md text off disk, i.e. the same
// pair the show endpoint answers with.
func localSkillView(st *skill.Store, name string) (client.SkillView, error) {
	s, ok, err := st.Get(name)
	if err != nil {
		return client.SkillView{}, err
	}
	if !ok {
		return client.SkillView{}, fmt.Errorf("%w: skill %q", skill.ErrNotFound, name)
	}
	content, err := os.ReadFile(filepath.Join(st.Dir(name), "SKILL.md"))
	if err != nil {
		return client.SkillView{}, fmt.Errorf("read SKILL.md of %q: %w", name, err)
	}
	return client.SkillView{Skill: s, Content: string(content)}, nil
}

// printSkillView renders `skill show`.
func printSkillView(c *gcli.Command, v client.SkillView) {
	c.Printf("name:          %s\n", v.Name)
	c.Printf("description:   %s\n", orDash(v.Description))
	c.Printf("source:        %s\n", orDash(v.Source))
	c.Printf("source_ref:    %s\n", orDash(v.SourceRef))
	c.Printf("version:       %s\n", orDash(v.Version))
	c.Printf("size:          %s\n", humanBytes(v.Size))
	c.Printf("updated_at:    %s\n", fmtServerTime(v.UpdatedAt))
	c.Printf("updated_by:    %s\n", orDash(v.UpdatedBy))
	if len(v.Files) == 0 {
		c.Println("files:         (none)")
	} else {
		c.Printf("files:         %d\n", len(v.Files))
		for _, f := range v.Files {
			c.Printf("  %-40s %-9s %s\n", f.Path, humanBytes(f.Size), shortSkillVersion(f.SHA256))
		}
	}
	c.Println("--- SKILL.md ---")
	if v.Content == "" {
		c.Println("(no SKILL.md content)")
		return
	}
	c.Println(strings.TrimRight(v.Content, "\n"))
}

// runAgentSkillImport imports a source into the library. In client mode a LOCAL
// source travels as a zip: a directory is archived into a temp .zip first (the server
// must never be handed a path into this machine), while a URL / git+https spec is
// resolved by the server itself.
func runAgentSkillImport(c *gcli.Command, args []string) error {
	src := toolArg(c, args, 0, "src")
	if src == "" {
		return fmt.Errorf("agent skill import requires a <src> argument")
	}
	if ch := agentSkillRemote(); ch.remote {
		return importSkillRemote(ch, c, src)
	}
	st, closeStore, err := openLocalSkillStore()
	if err != nil {
		return err
	}
	defer closeStore()
	s, err := st.Import(src, agentSkillCaller)
	if err != nil {
		return err
	}
	c.Printf("imported skill %s (version %s, %d files, %s)\n",
		s.Name, shortSkillVersion(s.Version), len(s.Files), humanBytes(s.Size))
	return nil
}

// importSkillRemote drives POST /v1/skills/import over HTTP: multipart for a local
// dir/.zip, JSON {"source":…} for something the server fetches itself.
func importSkillRemote(ch serverAPIChoice, c *gcli.Command, src string) error {
	cli, err := ch.client()
	if err != nil {
		return err
	}
	var res client.SkillImportResult
	if isRemoteSkillSrc(src) {
		res, err = cli.SkillImportSource(src)
	} else {
		archive, cleanup, aerr := localSkillArchive(src)
		if aerr != nil {
			return aerr
		}
		defer cleanup()
		res, err = cli.SkillImportFile(archive)
	}
	if err != nil {
		return ch.wrap(err)
	}
	note := ""
	if res.Replaced {
		note = " (replaced the previous copy)"
	}
	c.Printf("imported skill %s (version %s, %d files, %s)%s\n",
		res.Skill.Name, shortSkillVersion(res.Skill.Version), len(res.Skill.Files), humanBytes(res.Skill.Size), note)
	return nil
}

// isRemoteSkillSrc reports whether a source spec is one the SERVER resolves
// (`http(s)://…`, `git+https://…#subdir`) rather than bytes this machine sends.
func isRemoteSkillSrc(src string) bool {
	return strings.HasPrefix(src, "http://") ||
		strings.HasPrefix(src, "https://") ||
		strings.HasPrefix(src, "git+")
}

// localSkillArchive turns a local source into something uploadable: a directory is
// zipped into a temp file (returned with the cleanup that removes it), a .zip is
// passed through untouched (cleanup is a no-op).
func localSkillArchive(src string) (string, func(), error) {
	info, err := os.Stat(src)
	if err != nil {
		return "", nil, fmt.Errorf("skill source %s: %w", src, err)
	}
	if info.IsDir() {
		return zipDirToTemp(src)
	}
	return src, func() {}, nil
}

// zipDirToTemp archives dir into a fresh temp .zip whose entry names are relative to
// dir (slash-separated), i.e. exactly the tree `skill import <dir>` would see.
func zipDirToTemp(dir string) (string, func(), error) {
	f, err := os.CreateTemp("", "gofer-skill-*.zip")
	if err != nil {
		return "", nil, fmt.Errorf("create temp archive: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }

	zw := zip.NewWriter(f)
	walkErr := filepath.WalkDir(dir, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		hdr, herr := zip.FileInfoHeader(info)
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		// Skills are knowledge, never programs: the archive carries 0644 for every
		// entry so the exec bit cannot ride along (the server strips it too).
		hdr.SetMode(0o644)
		hdr.Method = zip.Deflate
		w, cerr := zw.CreateHeader(hdr)
		if cerr != nil {
			return cerr
		}
		src, oerr := os.Open(p)
		if oerr != nil {
			return oerr
		}
		defer src.Close()
		_, cerr = io.Copy(w, src)
		return cerr
	})
	if walkErr != nil {
		_ = zw.Close()
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("archive %s: %w", dir, walkErr)
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, fmt.Errorf("close archive: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("write archive: %w", err)
	}
	return path, cleanup, nil
}

// runAgentSkillUpdate re-fetches a skill from its recorded source and prints what
// the re-fetch changed, file by file.
func runAgentSkillUpdate(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return fmt.Errorf("agent skill update requires a <name> argument")
	}
	if ch := agentSkillRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		res, err := cli.SkillUpdate(name)
		if err != nil {
			return ch.wrap(err)
		}
		printSkillChange(c, res.Skill, res.Change)
		return nil
	}
	st, closeStore, err := openLocalSkillStore()
	if err != nil {
		return err
	}
	defer closeStore()
	ch, err := st.Update(name, agentSkillCaller)
	if err != nil {
		return err
	}
	updated, ok, err := st.Get(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w: skill %q", skill.ErrNotFound, name)
	}
	printSkillChange(c, updated, ch)
	return nil
}

// printSkillChange renders an update's outcome: the new version plus the added /
// changed / removed paths.
func printSkillChange(c *gcli.Command, s skill.Skill, ch skill.Change) {
	c.Printf("updated skill %s (version %s, %s)\n", ch.Name, shortSkillVersion(s.Version), humanBytes(s.Size))
	printSkillChangePaths(c, "added", ch.Added)
	printSkillChangePaths(c, "changed", ch.Changed)
	printSkillChangePaths(c, "removed", ch.Removed)
	if len(ch.Added)+len(ch.Changed)+len(ch.Removed) == 0 {
		c.Println("no file changes")
	}
}

func printSkillChangePaths(c *gcli.Command, label string, paths []string) {
	for _, p := range paths {
		c.Printf("  %-8s %s\n", label+":", p)
	}
}

// runAgentSkillRemove deletes a skill and its files.
func runAgentSkillRemove(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return fmt.Errorf("agent skill rm requires a <name> argument")
	}
	if ch := agentSkillRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		if err := cli.SkillRemove(name); err != nil {
			return ch.wrap(err)
		}
		c.Printf("removed skill %s\n", name)
		return nil
	}
	st, closeStore, err := openLocalSkillStore()
	if err != nil {
		return err
	}
	defer closeStore()
	if err := st.Remove(name); err != nil {
		return err
	}
	c.Printf("removed skill %s\n", name)
	return nil
}

// runAgentSkillExport writes the skill's archive to --out (default <name>.zip in the
// cwd). The archive is built at a temp path and renamed only once it is complete, so
// a failed export never leaves a truncated .zip that looks importable.
func runAgentSkillExport(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return fmt.Errorf("agent skill export requires a <name> argument")
	}
	out := strings.TrimSpace(agentSkillOpts.out)
	if out == "" {
		out = name + ".zip"
	}

	if ch := agentSkillRemote(); ch.remote {
		cli, err := ch.client()
		if err != nil {
			return err
		}
		if err := writeFileAtomic(out, func(w io.Writer) error { return cli.SkillExport(name, w) }); err != nil {
			return ch.wrap(err)
		}
	} else {
		st, closeStore, err := openLocalSkillStore()
		if err != nil {
			return err
		}
		defer closeStore()
		if err := writeFileAtomic(out, func(w io.Writer) error { return st.Export(name, w) }); err != nil {
			return err
		}
	}
	c.Printf("exported skill %s to %s\n", name, out)
	return nil
}

// writeFileAtomic runs write into a sibling temp file and renames it onto path on
// success, removing the temp file on any failure.
func writeFileAtomic(path string, write func(io.Writer) error) error {
	tmp := path + ".gofer-part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if werr := write(f); werr != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return werr
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// shortSkillVersion is the 12-char prefix of a sha256 (content hash or file digest)
// used in table output; `show` prints the full value.
func shortSkillVersion(v string) string {
	if v == "" {
		return "-"
	}
	if len(v) > 12 {
		return v[:12]
	}
	return v
}

// orDash renders an empty metadata field as "-" so a column never reads as a blank.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}
