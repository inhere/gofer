package store

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestLog writes content as jobID's log for stream, where ReadLogHead reads it.
func writeTestLog(t *testing.T, fs *FileStore, jobID string, stream Stream, content string) {
	t.Helper()
	name, err := logFileName(stream)
	if err != nil {
		t.Fatal(err)
	}
	dir := fs.Dir(jobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLogHeadWindow(t *testing.T) {
	const id = "20260912-000000-abcdef12"
	cases := []struct {
		name    string
		content string
		lines   int
		want    string
		total   int
	}{
		{"fewer than total", "a\nb\nc\n", 2, "a\nb\n", 3},
		{"exactly total", "a\nb\nc\n", 3, "a\nb\nc\n", 3},
		{"more than total", "a\nb\n", 10, "a\nb\n", 2},
		{"no trailing newline, cut before last", "a\nb\nc", 2, "a\nb\n", 3},
		{"no trailing newline, whole", "a\nb\nc", 3, "a\nb\nc", 3},
		{"empty file", "", 5, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := NewFileStore(t.TempDir())
			writeTestLog(t, fs, id, StreamStdout, tc.content)
			got, total, err := fs.ReadLogHead(id, StreamStdout, tc.lines)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want || total != tc.total {
				t.Fatalf("ReadLogHead(%d) = %q, total %d; want %q, total %d", tc.lines, got, total, tc.want, tc.total)
			}
		})
	}
}

func TestLogHeadStderrAndMissingFile(t *testing.T) {
	const id = "20260912-000000-abcdef13"
	fs := NewFileStore(t.TempDir())
	got, total, err := fs.ReadLogHead(id, StreamStderr, 3)
	if err != nil || len(got) != 0 || total != 0 {
		t.Fatalf("missing log = %q, %d, %v; want empty, 0, nil", got, total, err)
	}
	writeTestLog(t, fs, id, StreamStderr, "e1\ne2\ne3\n")
	got, total, err = fs.ReadLogHead(id, StreamStderr, 1)
	if err != nil || string(got) != "e1\n" || total != 3 {
		t.Fatalf("stderr head = %q, %d, %v; want \"e1\\n\", 3, nil", got, total, err)
	}
}
