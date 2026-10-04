package sessionrelay

import (
	"fmt"
	"strings"
	"unicode/utf16"

	"github.com/inhere/gofer/internal/jobstore"
)

// Wake-up directory decision (M8, bd tools-jqk).
//
// `claude --resume <id>` looks the session up under
// ~/.claude/projects/<encoded start cwd>/<id>.jsonl, where the directory is the
// cwd the session was STARTED in. The registered cwd is only whatever the last
// register carried (a SessionStart in the middle of a session may fire from a
// temporary worktree that is gone by the time somebody wakes the session), so the
// transcript path the hook registered — which IS that jsonl file — is the better
// witness of the original directory.
//
// The encoding is lossy (every non-alphanumeric character becomes `-`), so the
// directory name is never DECODED. Instead each candidate directory is encoded and
// compared with the name: a candidate that encodes to it is a verified original.

// Wake-up directory sources (CwdChoice.Source).
const (
	CwdSourceTranscript  = "transcript"   // verified against the transcript's directory name
	CwdSourceRegistered  = "registered"   // the cwd the session registered
	CwdSourceProjectRoot = "project_root" // the project root as the runner sees it
)

// CwdChoice is the directory a wake-up process will start in, and why.
type CwdChoice struct {
	// Rel is project-RELATIVE (what the job service SafeJoins on the runner).
	Rel string
	// Abs is Rel resolved against the runner's project root.
	Abs string
	// Source is one of the CwdSource* constants.
	Source string
	// Reason is the plain-language (Chinese) explanation of the choice.
	Reason string
}

// EncodeClaudeProjectDir is how Claude Code names a project's directory under
// ~/.claude/projects: every character that is not an ASCII letter or digit becomes
// "-" (one per UTF-16 code unit, as the JS regex it uses does), so "/d/work/a.b"
// is "-d-work-a-b" and "D:\work\x" is "D--work-x".
func EncodeClaudeProjectDir(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			for range utf16.RuneLen(r) {
				b.WriteByte('-')
			}
		}
	}
	return b.String()
}

// TranscriptProjectDirName extracts the directory name a transcript path lives in
// (".../projects/<name>/<id>.jsonl" → name). The path is as the REGISTERING machine
// saw it, so both separators are understood. "" when the path has no such shape.
func TranscriptProjectDirName(transcript string) string {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return ""
	}
	parts := strings.FieldsFunc(transcript, func(r rune) bool { return r == '/' || r == '\\' })
	if len(parts) < 3 {
		return ""
	}
	name, parent := parts[len(parts)-2], parts[len(parts)-3]
	if !strings.EqualFold(parent, "projects") || name == "" {
		return ""
	}
	return name
}

// isWinPath reports whether p is written as a Windows path (drive letter or
// backslashes); such paths compare case-insensitively.
func isWinPath(p string) bool {
	if strings.Contains(p, `\`) {
		return true
	}
	return len(p) >= 2 && p[1] == ':' && (p[0]|0x20 >= 'a' && p[0]|0x20 <= 'z')
}

// splitPath cuts p into its root prefix ("/" , "D:\" style drive prefix or "") and
// its elements, understanding both separators.
func splitPath(p string) (prefix string, elems []string, win bool) {
	p = strings.TrimSpace(p)
	win = isWinPath(p)
	rest := p
	switch {
	case win && len(p) >= 2 && p[1] == ':':
		prefix, rest = p[:2], p[2:]
	case strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`):
		prefix = "/"
	}
	elems = strings.FieldsFunc(rest, func(r rune) bool { return r == '/' || r == '\\' })
	return prefix, elems, win
}

func joinPath(prefix string, elems []string, win bool) string {
	sep := "/"
	if win {
		sep = `\`
	}
	switch {
	case prefix == "/":
		return "/" + strings.Join(elems, sep)
	case prefix != "": // drive
		return prefix + sep + strings.Join(elems, sep)
	}
	return strings.Join(elems, sep)
}

// pathAncestors lists p itself, then each parent directory up to (excluding) the
// filesystem root, in the spelling p used.
func pathAncestors(p string) []string {
	prefix, elems, win := splitPath(p)
	if len(elems) == 0 {
		return nil
	}
	out := make([]string, 0, len(elems))
	for n := len(elems); n >= 1; n-- {
		out = append(out, joinPath(prefix, elems[:n], win))
	}
	return out
}

// relUnder returns p relative to root ("." when equal), comparing element-wise,
// case-insensitively for Windows-style paths. ok is false when p is not under root.
func relUnder(root, p string) (string, bool) {
	rp, re, rwin := splitPath(root)
	pp, pe, pwin := splitPath(p)
	if len(re) == 0 && rp == "" {
		return "", false
	}
	fold := rwin || pwin
	eq := func(a, b string) bool {
		if fold {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	if !eq(strings.TrimSuffix(rp, "/"), strings.TrimSuffix(pp, "/")) || len(pe) < len(re) {
		return "", false
	}
	for i := range re {
		if !eq(re[i], pe[i]) {
			return "", false
		}
	}
	if len(pe) == len(re) {
		return ".", true
	}
	return strings.Join(pe[len(re):], "/"), true
}

// execJoin resolves a project-relative path against the runner's project root.
func execJoin(execRoot, rel string) string {
	if rel == "." || rel == "" {
		return execRoot
	}
	prefix, elems, win := splitPath(execRoot)
	return joinPath(prefix, append(elems, strings.Split(rel, "/")...), win)
}

// ChooseResumeCwd decides where a wake-up process starts (see the file comment):
//
//  1. a directory VERIFIED by the transcript's directory name — candidates are the
//     registered cwd and its ancestors, then the project root views — that is
//     under the project and (where the runner can be asked) still exists;
//  2. the registered cwd, when it is under the project and exists;
//  3. the project root as the runner sees it, when the registered cwd was in the
//     project but is gone.
//
// ok is false when the registered cwd is not under the project at all and nothing
// verified (the relay refuses with cwd_outside_project rather than guess).
func ChooseResumeCwd(a jobstore.AgentSession, plan TakeoverPlan) (CwdChoice, bool) {
	roots := make([]string, 0, 1+len(plan.AltRoots))
	for _, r := range append([]string{plan.ExecRoot}, plan.AltRoots...) {
		if strings.TrimSpace(r) != "" {
			roots = append(roots, r)
		}
	}
	if len(roots) == 0 {
		return CwdChoice{}, false
	}
	relOf := func(p string) (string, bool) {
		for _, r := range roots {
			if rel, ok := relUnder(r, p); ok {
				return rel, true
			}
		}
		return "", false
	}
	// state reports an exec-side path's existence: known=false when the runner
	// cannot be asked (a worker).
	state := func(abs string) (exists, known bool) {
		if plan.DirExists == nil {
			return false, false
		}
		return plan.DirExists(abs)
	}
	verb := func(abs string) string {
		if _, known := state(abs); !known {
			return "（执行机在远端，未核对该目录是否存在）"
		}
		return "（已确认存在）"
	}

	tdir := TranscriptProjectDirName(a.Transcript)
	cwd := strings.TrimSpace(a.Cwd)
	if tdir != "" {
		cands := pathAncestors(cwd)
		cands = append(cands, roots...)
		for _, cand := range cands {
			enc := EncodeClaudeProjectDir(cand)
			if enc != tdir && !(isWinPath(cand) && strings.EqualFold(enc, tdir)) {
				continue
			}
			rel, ok := relOf(cand)
			if !ok {
				continue
			}
			abs := execJoin(plan.ExecRoot, rel)
			if exists, known := state(abs); known && !exists {
				continue
			}
			reason := fmt.Sprintf("会话文件所在目录名 %q 验证出会话启动时的目录是 %s %s", tdir, abs, verb(abs))
			if cwd != "" && cand != cwd {
				reason += fmt.Sprintf("；登记的目录 %s 与它不同（可能是会话中途登记的临时目录），已忽略", cwd)
			}
			return CwdChoice{Rel: rel, Abs: abs, Source: CwdSourceTranscript, Reason: reason}, true
		}
	}

	why := "会话没有登记会话文件路径，无法验证原始启动目录"
	if tdir != "" {
		why = fmt.Sprintf("会话文件目录名 %q 与登记目录、项目根都对不上，无法验证原始启动目录", tdir)
	}
	rel, inProject := relOf(cwd)
	if !inProject {
		return CwdChoice{}, false
	}
	abs := execJoin(plan.ExecRoot, rel)
	if exists, known := state(abs); !known || exists {
		return CwdChoice{Rel: rel, Abs: abs, Source: CwdSourceRegistered,
			Reason: fmt.Sprintf("%s；使用登记的目录 %s %s", why, abs, verb(abs))}, true
	}
	root := execJoin(plan.ExecRoot, ".")
	return CwdChoice{Rel: ".", Abs: root, Source: CwdSourceProjectRoot,
		Reason: fmt.Sprintf("%s；登记的目录 %s 在执行机上已不存在，回落到项目根 %s", why, abs, root)}, true
}
