// Package skill is the gofer-side skill library (JOB-10, design
// docs/design/2026-09-23-skills-binding-and-comment-routing-design.md §一): a
// skill is a directory of "working method" knowledge — a required SKILL.md plus
// attachments — that lives WITH THE SERVER, not in the project tree, so a job can
// be handed it without a concurrent job seeing it and without git ever tracking
// it.
//
// One directory per skill, under a root the caller chooses
// (<config-dir>/skills by convention):
//
//	<root>/<name>/SKILL.md          required
//	<root>/<name>/<any attachment>  scripts, references, templates…
//
// The library is a directory tree; the SQLite index (the Repo seam) only holds
// metadata plus a sha256 per file, so `skill ls` / `skill update` never have to
// re-hash a skill to answer "what is in the library" or "what changed upstream".
//
// Three properties are deliberate and load-bearing:
//
//   - A skill is knowledge, not a program. Import drops every exec bit (0644) and
//     refuses symlinks; the agent runs a script by naming an interpreter, exactly
//     like any other file it reads.
//   - An archive is untrusted input. Every extracted path is resolved through
//     safeJoin (escape refused, absolute/drive-letter names refused on every
//     platform), and per-file / total byte caps are enforced while reading, not
//     from the archive's own size fields.
//   - A failed import never damages the library. Everything is materialised and
//     validated inside a temp dir UNDER the store root first (so the final step is
//     a same-filesystem rename); only then does it replace the previous copy, and
//     the index row is written after the files are in place (Store.publish).
//
// The package depends on nothing but the standard library and internal/util — the
// index is an interface (Repo), not a SQLite call — so it can be unit-tested
// without a database and adopted by the data layer without an import cycle.
// JobstoreRepo is the production wiring (jobstore's `skills` table).
package skill
