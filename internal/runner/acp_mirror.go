package runner

import (
	"os"
	"path/filepath"
)

// ACPArtifactPath is where a job's structured ACP stream lives under its result
// dir: the acp runner writes it there on the executing machine, and a hub mirrors a
// worker job's copy to the same place on the HOST (gofer-e2x7), so every reader
// (/v1/jobs/{id}/acp/stream) resolves one path regardless of where the job ran.
func ACPArtifactPath(resultDir string) string {
	return filepath.Join(resultDir, "artifacts", "acp.jsonl")
}

// AppendACPMirror appends one mirrored chunk of a remote job's acp.jsonl to the host
// job's copy under resultDir, creating the artifacts dir on first use. The file is
// opened per chunk on purpose: a mirror chunk is rare (a few per turn) and the sinks
// that call this (a dispatched job's sink, an adopted job's sink after a serve
// restart) then need no close hook tied to their own lifetime. It returns the bytes
// that landed; an empty resultDir or text is a no-op.
func AppendACPMirror(resultDir, text string) (int, error) {
	if resultDir == "" || text == "" {
		return 0, nil
	}
	path := ACPArtifactPath(resultDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return 0, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	n, werr := f.WriteString(text)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return n, werr
}
