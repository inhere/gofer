package sessionrelay

import (
	"slices"
	"strings"
	"testing"

	"github.com/inhere/gofer/internal/jobstore"
)

func TestEncodeClaudeProjectDir(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/d/work/inhere/x": "-d-work-inhere-x",
		`D:\work\inhere\x`: "D--work-inhere-x",
		"D:/work/x":        "D--work-x",
		"/a/.hidden/c_d e": "-a--hidden-c-d-e",
		"/home/u/proj.v2":  "-home-u-proj-v2",
		"/项目/a":            "----a", // one dash per UTF-16 code unit
		"/tmp/Test123/001": "-tmp-Test123-001",
		"/😀/a":             "----a", // an astral rune is two UTF-16 units
	}
	for in, want := range cases {
		if got := EncodeClaudeProjectDir(in); got != want {
			t.Errorf("EncodeClaudeProjectDir(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranscriptProjectDirName(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"/home/u/.claude/projects/-d-work-x/abc.jsonl":    "-d-work-x",
		`C:\Users\u\.claude\projects\D--work-x\abc.jsonl`: "D--work-x",
		"C:/Users/u/.claude/projects/D--work-x/abc.jsonl": "D--work-x",
		"/home/u/.claude/Projects/-d-work-x/abc.jsonl":    "-d-work-x",
		"":                               "",
		"abc.jsonl":                      "",
		"/tmp/other/-d-work-x/abc.jsonl": "", // not under projects/
	}
	for in, want := range cases {
		if got := TranscriptProjectDirName(in); got != want {
			t.Errorf("TranscriptProjectDirName(%q) = %q, want %q", in, got, want)
		}
	}
}

// existsSet is a fake runner filesystem: only the listed directories exist.
func existsSet(dirs ...string) func(string) (bool, bool) {
	return func(p string) (bool, bool) { return slices.Contains(dirs, p), true }
}

func sess(cwd, transcript string) jobstore.AgentSession {
	return jobstore.AgentSession{SessionID: "sid", Agent: "claude", Cwd: cwd, Transcript: transcript}
}

func TestChooseResumeCwd(t *testing.T) {
	t.Parallel()
	type tc struct {
		name       string
		a          jobstore.AgentSession
		plan       TakeoverPlan
		wantOK     bool
		wantRel    string
		wantAbs    string
		wantSource string
		reasonHas  string
	}
	cases := []tc{
		{
			name: "windows drive: deleted temp worktree falls back to the original project dir",
			a: sess(`D:\work\proj\.worktrees\tmp1`,
				`C:\Users\u\.claude\projects\D--work-proj\sid.jsonl`),
			plan:       TakeoverPlan{ExecRoot: `D:\work\proj`, DirExists: existsSet(`D:\work\proj`)},
			wantOK:     true,
			wantRel:    ".",
			wantAbs:    `D:\work\proj`,
			wantSource: CwdSourceTranscript,
			reasonHas:  "已忽略",
		},
		{
			name: "dot in a directory name: the original is a sub directory under a dot dir",
			a: sess("/d/work/proj/.hidden/sub/deeper",
				"/root/.claude/projects/-d-work-proj--hidden-sub/sid.jsonl"),
			plan:       TakeoverPlan{ExecRoot: "/d/work/proj", DirExists: existsSet("/d/work/proj", "/d/work/proj/.hidden/sub")},
			wantOK:     true,
			wantRel:    ".hidden/sub",
			wantAbs:    "/d/work/proj/.hidden/sub",
			wantSource: CwdSourceTranscript,
		},
		{
			name: "container /d/work path executed on the server, session recorded in the host view",
			a: sess(`D:\work\proj\wt`,
				"/root/.claude/projects/D--work-proj/sid.jsonl"),
			plan: TakeoverPlan{ExecRoot: "/d/work/proj", AltRoots: []string{"D:/work/proj"},
				DirExists: existsSet("/d/work/proj")},
			wantOK:     true,
			wantRel:    ".",
			wantAbs:    "/d/work/proj",
			wantSource: CwdSourceTranscript,
		},
		{
			name: "transcript dir matches the registered cwd itself",
			a: sess("/d/work/proj/pkg",
				"/root/.claude/projects/-d-work-proj-pkg/sid.jsonl"),
			plan:       TakeoverPlan{ExecRoot: "/d/work/proj", DirExists: existsSet("/d/work/proj", "/d/work/proj/pkg")},
			wantOK:     true,
			wantRel:    "pkg",
			wantAbs:    "/d/work/proj/pkg",
			wantSource: CwdSourceTranscript,
		},
		{
			name: "negative: transcript name matches no candidate, the existing registered cwd is used",
			a: sess("/d/work/proj/pkg",
				"/root/.claude/projects/-somewhere-else/sid.jsonl"),
			plan:       TakeoverPlan{ExecRoot: "/d/work/proj", DirExists: existsSet("/d/work/proj", "/d/work/proj/pkg")},
			wantOK:     true,
			wantRel:    "pkg",
			wantAbs:    "/d/work/proj/pkg",
			wantSource: CwdSourceRegistered,
			reasonHas:  "对不上",
		},
		{
			name:       "negative: no transcript registered, the registered cwd is used",
			a:          sess("/d/work/proj/pkg", ""),
			plan:       TakeoverPlan{ExecRoot: "/d/work/proj", DirExists: existsSet("/d/work/proj/pkg")},
			wantOK:     true,
			wantRel:    "pkg",
			wantAbs:    "/d/work/proj/pkg",
			wantSource: CwdSourceRegistered,
			reasonHas:  "没有登记会话文件",
		},
		{
			name:       "registered cwd gone and nothing verifies: fall back to the project root",
			a:          sess("/d/work/proj/gone", ""),
			plan:       TakeoverPlan{ExecRoot: "/d/work/proj", DirExists: existsSet("/d/work/proj")},
			wantOK:     true,
			wantRel:    ".",
			wantAbs:    "/d/work/proj",
			wantSource: CwdSourceProjectRoot,
			reasonHas:  "已不存在",
		},
		{
			name: "the verified original is itself deleted: registered, then project root",
			a: sess("/d/work/proj/wt1",
				"/root/.claude/projects/-d-work-proj-wt1/sid.jsonl"),
			plan:       TakeoverPlan{ExecRoot: "/d/work/proj", DirExists: existsSet("/d/work/proj")},
			wantOK:     true,
			wantRel:    ".",
			wantAbs:    "/d/work/proj",
			wantSource: CwdSourceProjectRoot,
		},
		{
			name:   "outside the project and unverifiable: refused",
			a:      sess("/elsewhere/x", "/root/.claude/projects/-elsewhere-x/sid.jsonl"),
			plan:   TakeoverPlan{ExecRoot: "/d/work/proj", DirExists: existsSet("/d/work/proj")},
			wantOK: false,
		},
		{
			name: "worker: no existence check, the verified original still wins and the text says so",
			a: sess("/d/work/proj/.wt/tmp",
				"/root/.claude/projects/-d-work-proj/sid.jsonl"),
			plan:       TakeoverPlan{ExecRoot: "/d/work/proj"},
			wantOK:     true,
			wantRel:    ".",
			wantAbs:    "/d/work/proj",
			wantSource: CwdSourceTranscript,
			reasonHas:  "未核对",
		},
		{
			name: "ambiguity: a-b and a/b encode alike, only a real candidate is accepted",
			a: sess("/d/work/a-b/x",
				"/root/.claude/projects/-d-work-a-b/sid.jsonl"),
			plan:       TakeoverPlan{ExecRoot: "/d/work/a-b", DirExists: existsSet("/d/work/a-b", "/d/work/a-b/x")},
			wantOK:     true,
			wantRel:    ".",
			wantAbs:    "/d/work/a-b",
			wantSource: CwdSourceTranscript,
		},
		{
			name: "windows drive letters compare case-insensitively",
			a: sess(`d:\work\proj\wt`,
				`C:\Users\u\.claude\projects\D--work-proj\sid.jsonl`),
			plan:       TakeoverPlan{ExecRoot: `D:\work\proj`, DirExists: existsSet(`D:\work\proj`)},
			wantOK:     true,
			wantRel:    ".",
			wantAbs:    `D:\work\proj`,
			wantSource: CwdSourceTranscript,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ChooseResumeCwd(c.a, c.plan)
			if ok != c.wantOK {
				t.Fatalf("ok=%v, want %v (%+v)", ok, c.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Rel != c.wantRel || got.Abs != c.wantAbs || got.Source != c.wantSource {
				t.Fatalf("got rel=%q abs=%q source=%q, want rel=%q abs=%q source=%q\nreason: %s",
					got.Rel, got.Abs, got.Source, c.wantRel, c.wantAbs, c.wantSource, got.Reason)
			}
			if got.Reason == "" || (c.reasonHas != "" && !strings.Contains(got.Reason, c.reasonHas)) {
				t.Fatalf("reason %q should mention %q", got.Reason, c.reasonHas)
			}
		})
	}
}
