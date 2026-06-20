package job

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/inhere/gofer/internal/runner"
)

// maxResultJSONBytes caps how large a <result_dir>/result.json may be before it
// is rejected from the DB (design §6.3 / D3): a larger file is left on disk but
// not inlined into the jobs.result_json column to keep the metadata DB small.
const maxResultJSONBytes = 256 * 1024

// captureHook is a test-only seam: when non-nil it runs inside captureOutcomes
// (under its recover guard). Tests set it to a panicking func to prove a failing
// capture is swallowed and never changes the job's terminal status (best-effort).
// It is nil in production.
var captureHook func(*jobEntry, runner.Request)

// captureOutcomes 在 job 跑完(终态前)采集产出，写入 entry.result 对应字段；
// best-effort：任何失败仅记 warning，绝不改变 job 终态（产出是附加审计信息，
// 不能让 result.json 解析失败把 done 变 failed）。设字段在 entry.result 上、
// 在 finish 之前 —— finish 的 snap := entry.result 会带上它们一并 persist，
// 无需改 finish 签名。
//
// 仅 local 有数据：远端(worker/peer)的 req.Command 为空、result_dir 在执行侧，
// P4 经回传补。本阶段(P1)实现 E15 渲染命令 + E6 结构化结果；E1 产物清单(P2) /
// E12 diff(P3) 的接入点以注释保留。
func (s *Service) captureOutcomes(entry *jobEntry, req runner.Request) {
	// best-effort 总闸：任何采集子步骤 panic 也被吞掉，绝不让产出采集影响 job 终态。
	defer func() {
		if r := recover(); r != nil {
			log.Printf("captureOutcomes: recovered panic for job %s: %v", req.JobID, r)
		}
	}()

	if captureHook != nil {
		captureHook(entry, req) // 测试注入：验证 best-effort（panic 被吞，job 仍 done）。
	}

	entry.mu.Lock()
	resultDir := entry.result.ResultDir
	cwd := entry.result.Cwd
	projectKey := entry.result.ProjectKey
	entry.mu.Unlock()

	rendered := renderedCommandJSON(req)  // E15 渲染命令
	result := readResultJSON(resultDir)   // E6 结构化结果
	artifacts := scanArtifacts(resultDir) // E1 产物清单

	// E12 diff 快照(P3)：项目 capture_diff 显式 false → 跳过；否则交给 captureDiff
	// 自身的 is-git 判定（非 git 仓自然返 ""）。全量写 changes.diff，--stat 摘要入库。
	var diffSummary string
	if s.shouldCaptureDiff(projectKey) {
		diffSummary = captureDiff(cwd, resultDir)
	}

	entry.mu.Lock()
	if rendered != "" {
		entry.result.RenderedCommand = rendered
	}
	if result != "" {
		entry.result.ResultJSON = result
	}
	if len(artifacts) > 0 {
		if b, err := json.Marshal(artifacts); err == nil {
			entry.result.ArtifactsJSON = string(b)
		} else {
			log.Printf("captureOutcomes: marshal artifacts for job %s: %v", req.JobID, err)
		}
	}
	if diffSummary != "" {
		entry.result.DiffSummary = diffSummary
	}
	entry.mu.Unlock()
}

// shouldCaptureDiff reports whether E12 git-diff capture is enabled for the job's
// project (P3). It resolves the ProjectConfig the same way the service does
// elsewhere — s.config().Projects[projectKey] — and honours the capture_diff
// toggle: an explicit false skips capture entirely; nil (unset) or true defers to
// captureDiff's own is-git probe (a non-git cwd then yields no diff). An unknown
// project (should not happen post-validate) also defers to the is-git probe.
func (s *Service) shouldCaptureDiff(projectKey string) bool {
	proj, ok := s.config().Projects[projectKey]
	if !ok {
		return true
	}
	if proj.CaptureDiff != nil && !*proj.CaptureDiff {
		return false
	}
	return true
}

// renderedCommandJSON 把本次实际执行的命令序列化为审计 JSON：
// {command, args, env_keys}。env 只存 KEY 名、不存值（防 secret 入库，
// SR403/SR805）；env_keys 排序保证稳定输出。远端 runner 的 req.Command 为空
// → 返回 ""（P4 由执行侧回传）。
func renderedCommandJSON(req runner.Request) string {
	if req.Command == "" {
		return ""
	}
	keys := make([]string, 0, len(req.Env))
	for k := range req.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, err := json.Marshal(struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		EnvKeys []string `json:"env_keys,omitempty"`
	}{req.Command, req.Args, keys})
	if err != nil {
		log.Printf("captureOutcomes: marshal rendered command for job %s: %v", req.JobID, err)
		return ""
	}
	return string(b)
}

// readResultJSON 读 <result_dir>/result.json（agent/wrapper 经 {{result_dir}}
// 模板写入）：不存在 → ""；超上限(maxResultJSONBytes)或非法 JSON → 记 warning 返
// ""（不污染 DB）。返回的字符串已是合法 JSON，get_job 透传后前端 JSON.parse。
func readResultJSON(resultDir string) string {
	if resultDir == "" {
		return ""
	}
	p := filepath.Join(resultDir, "result.json")
	fi, err := os.Stat(p)
	if err != nil {
		// 不存在是常态(多数 job 不写 result.json)，不记 warning。
		return ""
	}
	if fi.IsDir() {
		return ""
	}
	if fi.Size() > maxResultJSONBytes {
		log.Printf("captureOutcomes: result.json %s too large (%d > %d), skipped", p, fi.Size(), maxResultJSONBytes)
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		log.Printf("captureOutcomes: read result.json %s: %v", p, err)
		return ""
	}
	if !json.Valid(b) {
		log.Printf("captureOutcomes: result.json %s is not valid JSON, skipped", p)
		return ""
	}
	return string(b)
}

// ArtifactItem is one entry in a job's产物清单 (E1): a regular file under
// <result_dir>/artifacts/. Name is the path relative to the artifacts dir
// (may contain subdir segments, slash-separated); only metadata is captured —
// the file itself stays on disk and is fetched via the download endpoint.
type ArtifactItem struct {
	Name  string `json:"name"`  // artifacts/ 下相对路径（可含子目录，'/' 分隔）
	Size  int64  `json:"size"`  // 字节
	Mtime int64  `json:"mtime"` // unix 秒
}

// maxArtifacts caps how many artifact entries are captured into the manifest:
// a job that writes a huge tree is truncated to keep the manifest (and DB
// column) bounded. When truncated the manifest still lists the first
// maxArtifacts entries; the rest are reachable on disk but not enumerated.
const maxArtifacts = 500

// ScanArtifacts is the exported entry point for the live-scan fallback used by
// the HTTP list endpoint when a job has no persisted manifest (ArtifactsJSON
// empty). It shares scanArtifacts' semantics (recursive, slash-relative names,
// skip dirs/symlinks, cap maxArtifacts, missing dir → nil).
func ScanArtifacts(resultDir string) []ArtifactItem { return scanArtifacts(resultDir) }

// scanArtifacts enumerates the regular files under <result_dir>/artifacts/
// (recursive, relative names). It is best-effort: a missing artifacts dir or a
// walk error yields nil. Directories and symlinks are skipped (symlinks never
// recursed and never listed, so a malicious symlink cannot inflate or escape
// the manifest). At most maxArtifacts entries are returned. Names use forward
// slashes regardless of platform so they round-trip into the download URL.
func scanArtifacts(resultDir string) []ArtifactItem {
	if resultDir == "" {
		return nil
	}
	base := filepath.Join(resultDir, "artifacts")
	fi, err := os.Stat(base)
	if err != nil || !fi.IsDir() {
		// 不存在/非目录是常态（多数 job 不产物），不记 warning。
		return nil
	}

	var items []ArtifactItem
	truncated := false
	walkErr := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// 单个条目读失败：跳过它，不中断整体扫描。
			return nil
		}
		if path == base {
			return nil
		}
		// 跳目录（仍递归进入）与软链（不 stat 目标、不列出、不跟随）。
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // 设备/管道/socket 等非常规文件不列。
		}
		if len(items) >= maxArtifacts {
			truncated = true
			return filepath.SkipAll
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return nil
		}
		items = append(items, ArtifactItem{
			Name:  filepath.ToSlash(rel),
			Size:  info.Size(),
			Mtime: info.ModTime().Unix(),
		})
		return nil
	})
	if walkErr != nil {
		log.Printf("captureOutcomes: scan artifacts %s: %v", base, walkErr)
	}
	if truncated {
		log.Printf("captureOutcomes: artifacts under %s exceed cap %d, manifest truncated", base, maxArtifacts)
	}
	return items
}
