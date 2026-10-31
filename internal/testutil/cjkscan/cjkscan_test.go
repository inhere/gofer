package cjkscan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHitsCountOnlyStringLiterals(t *testing.T) {
	dir := t.TempDir()
	src := "package x\n\n// 注释里的中文不算\nvar a = \"plain\"\nvar b = \"中文\"\nconst c = `多行\n文本`\n"
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a_test.go"), []byte("package x\nvar d = \"测试\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Hits(t, dir)
	if len(got) != 1 || len(got["a.go"]) != 2 {
		t.Fatalf("hits = %v, want only a.go with 2 literals", got)
	}
}
