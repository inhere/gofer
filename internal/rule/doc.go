// Package rule is the gofer RULE library (JOB-06①, design §一.1): a rule is a short,
// MANDATORY piece of discipline the server owns and injects at the TOP of every job
// prompt that binds it ("改文件只用 apply_patch", "测试先写先提交"). It is deliberately
// the opposite of a skill (internal/skill): a skill is optional reading material the
// agent may open, a rule is text that is always placed in front of the agent.
//
// Storage mirrors the skill library on purpose: <config-dir>/rules/<name>.md plus a
// metadata index (this package's Repo seam, implemented by internal/jobstore). The
// text lives in a file so an operator can edit it with any editor and back it up
// with the rest of the config; the index is only what `agent rule ls` and the
// binding resolver need.
//
// It imports the standard library plus internal/template's frontmatter splitter — no
// gofer package depends on it in return, so the job service (which injects) and the
// HTTP/CLI surfaces (which manage) can both use it without an import cycle (G022).
package rule
