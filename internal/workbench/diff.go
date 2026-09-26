package workbench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/job"
	"github.com/inhere/gofer/internal/jobstore"
)

const (
	threadDiffTimeout  = 10 * time.Second
	maxThreadDiffBytes = 2 * 1024 * 1024
)

// Diff returns the changes made by a thread since its first recorded Git base.
// Git is consulted only for a server-local latest turn whose cwd still exists;
// remote and vanished working directories use the latest captured artifact.
func (s *Service) Diff(threadID string) (ThreadDiff, error) {
	if s == nil || s.store == nil {
		return ThreadDiff{}, ErrUnavailable
	}
	records, err := s.threadJobRecords(threadID)
	if err != nil {
		return ThreadDiff{}, err
	}
	first, latest := records[0], records[len(records)-1]
	base := strings.TrimSpace(first.WorktreeBaseSHA)
	if base == "" {
		base = strings.TrimSpace(first.BaseSHA)
	}
	if base == "" {
		return ThreadDiff{}, fmt.Errorf("%w: 首轮作业没有 base_sha/worktree_base_sha，无法建立会话改动基线", ErrMissingDiffBase)
	}

	if config.IsBuiltinLocalRunnerName(latest.Runner) && isDirectory(latest.Cwd) {
		return liveThreadDiff(latest.Cwd, base)
	}
	return capturedThreadDiff(latest, base)
}

func (s *Service) threadJobRecords(threadID string) ([]jobstore.JobRecord, error) {
	kind, rawID, err := ParseThreadID(threadID)
	if err != nil {
		return nil, err
	}
	var records []jobstore.JobRecord
	switch kind {
	case KindAgent:
		records, err = s.store.ListJobs(jobstore.ListQuery{Session: rawID, Limit: 1000})
	case KindJob:
		var ok bool
		var record jobstore.JobRecord
		record, ok, err = s.store.GetJob(rawID)
		if err == nil && ok {
			records = []jobstore.JobRecord{record}
		} else if err == nil {
			return nil, ErrUnknownThread
		}
	case KindRelay:
		return nil, fmt.Errorf("%w: relay 会话没有可审阅的 job 改动", ErrNotResumable)
	default:
		return nil, ErrInvalidThreadID
	}
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, ErrUnknownThread
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].StartedAt != records[j].StartedAt {
			return records[i].StartedAt < records[j].StartedAt
		}
		return records[i].ID < records[j].ID
	})
	return records, nil
}

func liveThreadDiff(cwd, base string) (ThreadDiff, error) {
	if !isHexObjectID(base) {
		return ThreadDiff{}, fmt.Errorf("%w: invalid base sha %q", ErrDiffUnavailable, base)
	}
	ctx, cancel := context.WithTimeout(context.Background(), threadDiffTimeout)
	defer cancel()

	headRaw, _, err := runThreadGit(ctx, cwd, 256, "rev-parse", "HEAD")
	if err != nil {
		return ThreadDiff{}, err
	}
	patchRaw, patchTruncated, err := runThreadGit(ctx, cwd, maxThreadDiffBytes,
		"-c", "core.quotepath=false", "diff", "--no-ext-diff", "--no-color", base, "--")
	if err != nil {
		return ThreadDiff{}, err
	}
	nameRaw, nameTruncated, err := runThreadGit(ctx, cwd, maxThreadDiffBytes,
		"-c", "core.quotepath=false", "diff", "--name-status", "-z", base, "--")
	if err != nil {
		return ThreadDiff{}, err
	}
	numRaw, numTruncated, err := runThreadGit(ctx, cwd, maxThreadDiffBytes,
		"-c", "core.quotepath=false", "diff", "--numstat", "-z", base, "--")
	if err != nil {
		return ThreadDiff{}, err
	}
	untrackedRaw, untrackedTruncated, err := runThreadGit(ctx, cwd, maxThreadDiffBytes,
		"-c", "core.quotepath=false", "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ThreadDiff{}, err
	}
	if nameTruncated || numTruncated || untrackedTruncated {
		return ThreadDiff{}, fmt.Errorf("%w: git file metadata exceeded 2 MiB", ErrDiffUnavailable)
	}
	files, err := mergeLiveDiffFiles(nameRaw, numRaw, untrackedRaw)
	if err != nil {
		return ThreadDiff{}, err
	}
	return ThreadDiff{
		Source: DiffSourceLive, Base: base, Head: strings.TrimSpace(string(headRaw)),
		Files: files, Patch: string(patchRaw), Truncated: patchTruncated,
	}, nil
}

func capturedThreadDiff(latest jobstore.JobRecord, base string) (ThreadDiff, error) {
	patch, truncated, err := readCappedFile(filepath.Join(latest.ResultDir, "changes.diff"), maxThreadDiffBytes)
	if err != nil && !os.IsNotExist(err) {
		return ThreadDiff{}, fmt.Errorf("%w: read captured changes.diff: %v", ErrDiffUnavailable, err)
	}
	if os.IsNotExist(err) {
		patch = nil
		truncated = false
	}
	commits := parseCommits(latest.CommitsJSON)
	head := strings.TrimSpace(latest.WorktreeHeadSHA)
	if head == "" && len(commits) > 0 {
		head = commits[0].SHA
	}
	return ThreadDiff{
		Source: DiffSourceCaptured, Base: base, Head: head,
		Files: parsePatchFiles(string(patch)), Patch: string(patch), Truncated: truncated,
		Commits: commits, Notice: "只含最新一轮采集结果",
	}, nil
}

func runThreadGit(ctx context.Context, cwd string, limit int, args ...string) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, fmt.Errorf("%w: git stdout: %v", ErrDiffUnavailable, err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("%w: start git: %v", ErrDiffUnavailable, err)
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, int64(limit)+1))
	_, drainErr := io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, false, fmt.Errorf("%w: git %s", ErrDiffTimeout, strings.Join(args, " "))
	}
	if readErr != nil || drainErr != nil {
		return nil, false, fmt.Errorf("%w: read git output", ErrDiffUnavailable)
	}
	if waitErr != nil {
		detail := strings.TrimSpace(stderr.String())
		return nil, false, fmt.Errorf("%w: git %s: %v: %s", ErrDiffUnavailable, strings.Join(args, " "), waitErr, detail)
	}
	truncated := len(data) > limit
	if truncated {
		data = trimValidUTF8(data[:limit])
	}
	return data, truncated, nil
}

func readCappedFile(path string, limit int) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, false, err
	}
	truncated := len(data) > limit
	if truncated {
		data = trimValidUTF8(data[:limit])
	}
	return data, truncated, nil
}

func trimValidUTF8(data []byte) []byte {
	for len(data) > 0 && !utf8.Valid(data) {
		data = data[:len(data)-1]
	}
	return data
}

func isDirectory(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isHexObjectID(value string) bool {
	if len(value) < 7 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func parseCommits(raw string) []job.Commit {
	var commits []job.Commit
	if json.Unmarshal([]byte(raw), &commits) != nil {
		return nil
	}
	return commits
}

func mergeLiveDiffFiles(nameRaw, numRaw, untrackedRaw []byte) ([]ThreadDiffFile, error) {
	files := make(map[string]ThreadDiffFile)
	order := make([]string, 0)
	for _, item := range parseNameStatus(nameRaw) {
		if _, exists := files[item.Path]; !exists {
			order = append(order, item.Path)
		}
		files[item.Path] = item
	}
	for path, stats := range parseNumstat(numRaw) {
		item, exists := files[path]
		if !exists {
			item = ThreadDiffFile{Path: path, Status: "M"}
			order = append(order, path)
		}
		item.Additions, item.Deletions, item.Binary = stats.Additions, stats.Deletions, stats.Binary
		files[path] = item
	}
	for _, path := range splitNUL(untrackedRaw) {
		if path == "" {
			continue
		}
		if _, exists := files[path]; !exists {
			order = append(order, path)
		}
		files[path] = ThreadDiffFile{Path: path, Status: "?"}
	}
	result := make([]ThreadDiffFile, 0, len(order))
	for _, path := range order {
		result = append(result, files[path])
	}
	return result, nil
}

func parseNameStatus(raw []byte) []ThreadDiffFile {
	fields := splitNUL(raw)
	result := make([]ThreadDiffFile, 0, len(fields)/2)
	for i := 0; i < len(fields); {
		status, path := "", ""
		token := fields[i]
		i++
		if tab := strings.IndexByte(token, '\t'); tab >= 0 {
			status, path = token[:tab], token[tab+1:]
		} else {
			status = token
			if i < len(fields) {
				path = fields[i]
				i++
			}
		}
		code := status
		if len(code) > 1 {
			code = code[:1]
		}
		if (code == "R" || code == "C") && i < len(fields) {
			path = fields[i]
			i++
		}
		if path != "" {
			result = append(result, ThreadDiffFile{Path: path, Status: code})
		}
	}
	return result
}

type diffStats struct {
	Additions int
	Deletions int
	Binary    bool
}

func parseNumstat(raw []byte) map[string]diffStats {
	fields := splitNUL(raw)
	result := make(map[string]diffStats)
	for i := 0; i < len(fields); i++ {
		parts := strings.Split(fields[i], "\t")
		if len(parts) < 3 {
			continue
		}
		path := strings.Join(parts[2:], "\t")
		if path == "" && i+2 < len(fields) {
			// With -z, a rename stores an empty path then old and new paths.
			path = fields[i+2]
			i += 2
		}
		if path == "" {
			continue
		}
		stats := diffStats{Binary: parts[0] == "-" || parts[1] == "-"}
		if !stats.Binary {
			stats.Additions, _ = strconv.Atoi(parts[0])
			stats.Deletions, _ = strconv.Atoi(parts[1])
		}
		result[path] = stats
	}
	return result
}

func splitNUL(raw []byte) []string {
	trimmed := bytes.TrimSuffix(raw, []byte{0})
	if len(trimmed) == 0 {
		return nil
	}
	parts := bytes.Split(trimmed, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		result = append(result, string(part))
	}
	return result
}

func parsePatchFiles(patch string) []ThreadDiffFile {
	var (
		files   []ThreadDiffFile
		current *ThreadDiffFile
	)
	for _, raw := range strings.Split(patch, "\n") {
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case strings.HasPrefix(line, "diff --git "):
			parts := strings.SplitN(strings.TrimPrefix(line, "diff --git "), " b/", 2)
			path := strings.TrimPrefix(parts[0], "a/")
			if len(parts) == 2 {
				path = parts[1]
			}
			files = append(files, ThreadDiffFile{Path: path, Status: "M"})
			current = &files[len(files)-1]
		case current == nil:
			continue
		case strings.HasPrefix(line, "new file mode "):
			current.Status = "A"
		case strings.HasPrefix(line, "deleted file mode "):
			current.Status = "D"
		case strings.HasPrefix(line, "rename to "):
			current.Status = "R"
			current.Path = strings.TrimPrefix(line, "rename to ")
		case strings.HasPrefix(line, "+++ b/"):
			current.Path = strings.TrimPrefix(line, "+++ b/")
		case strings.HasPrefix(line, "--- a/") && current.Status == "D":
			current.Path = strings.TrimPrefix(line, "--- a/")
		case strings.HasPrefix(line, "Binary files ") || line == "GIT binary patch":
			current.Binary = true
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			current.Additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			current.Deletions++
		}
	}
	if files == nil {
		return []ThreadDiffFile{}
	}
	return files
}
