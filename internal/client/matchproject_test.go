package client

import "testing"

func TestMatchProjectPath(t *testing.T) {
	projects := []ProjectMeta{
		{Key: "ws", HostPath: "/srv/ws", ContainerPath: "/work/ws"},
		{Key: "inner", HostPath: "/srv/ws/tools/inner"},
		{Key: "win", HostPath: `D:\code\win`},
		{Key: "dupa", HostPath: "/srv/dup"},
		{Key: "dupb", ContainerPath: "/srv/dup"},
	}
	cases := []struct {
		cwd, key string
		ok       bool
	}{
		{"/srv/ws", "ws", true},
		{"/work/ws/pkg/x", "ws", true},           // container view
		{"/srv/ws/tools/inner/a", "inner", true}, // longest prefix beats the outer project
		{"/srv/ws/tools", "ws", true},
		{"/srv/wsx", "", false}, // not a path boundary
		{"D:/code/win/sub", "win", true},
		{"/srv/dup/a", "", false}, // tie between two projects
		{"/elsewhere", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		key, _, ok := MatchProjectPath(projects, c.cwd)
		if key != c.key || ok != c.ok {
			t.Errorf("cwd %q: got (%q,%v) want (%q,%v)", c.cwd, key, ok, c.key, c.ok)
		}
	}
}
