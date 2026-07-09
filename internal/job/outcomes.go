package job

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/inhere/gofer/internal/store"

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
// 分流(P4)：远端 runner(worker/peer)已在执行机本地采集产出并经 res.Outcome 回传
// → 直接 applyOutcome 落库（host 不持有远端 result_dir，无从磁盘扫描）。本地 runner
// res.Outcome 为 nil → 走 P1–P3 的本地磁盘扫描（E15 渲染命令 / E6 结构化结果 /
// E1 产物清单 / E12 diff）。
func (s *Service) captureOutcomes(entry *jobEntry, req runner.Request, res runner.Result) {
	// best-effort 总闸：任何采集子步骤 panic 也被吞掉，绝不让产出采集影响 job 终态。
	defer func() {
		if r := recover(); r != nil {
			slog.Warn("captureOutcomes: recovered panic", "job_id", req.JobID, "panic", r)
		}
	}()

	if captureHook != nil {
		captureHook(entry, req) // 测试注入：验证 best-effort（panic 被吞，job 仍 done）。
	}

	// 远端：执行机已 capture 并经 res.Outcome 回传 → 直接落，不再扫盘（P4）。
	if res.Outcome != nil {
		s.applyOutcome(entry, res.Outcome)
		return
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
			slog.Warn("captureOutcomes: marshal artifacts", "job_id", req.JobID, "err", err)
		}
	}
	if diffSummary != "" {
		entry.result.DiffSummary = diffSummary
	}
	entry.mu.Unlock()

	// session 捕获(模式②, session-capture §5.1)：仅当 session_id 仍为空时尝试——注入式
	// (claude) 已在提交时填好、显式 SessionID(resume) 也已带上，都跳过。codex 默认输出
	// 头部 `session id:`，按 agent.SessionCapture 正则先扫 stdout.log，未命中再扫
	// stderr.log；再不行读 <result_dir>/session_id 文件兜底(选项C)。整段 best-effort，
	// 在 captureOutcomes 的 recover 总闸内、绝不影响 job 终态。
	s.captureSession(entry, resultDir)
}

// captureSession 在本地终态尝试为未注入会话的 job 捕获 session_id（模式②）。它读
// entry.result 的 SessionID/Agent，仅当 SessionID 为空才动作：先按该 agent 的
// SessionCapture 正则扫 <result_dir>/stdout.log，再扫 stderr.log，命中即填；否则读
// <result_dir>/session_id 文件兜底（选项C）。任何失败静默返回（不改终态）。
func (s *Service) captureSession(entry *jobEntry, resultDir string) {
	entry.mu.Lock()
	sid := entry.result.SessionID
	agentKey := entry.result.Agent
	entry.mu.Unlock()
	if sid != "" {
		return // 注入式/显式已知，不捕获。
	}

	var captured string
	if ac, ok := s.agents.Get(agentKey); ok && ac.SessionCapture != "" {
		captured = captureSessionID(filepath.Join(resultDir, store.StdoutFile), ac.SessionCapture)
		if captured == "" {
			captured = captureSessionID(filepath.Join(resultDir, store.StderrFile), ac.SessionCapture)
		}
	}
	if captured == "" && resultDir != "" { // 选项C 兜底：任务自写的 session_id 文件。
		if b, err := os.ReadFile(filepath.Join(resultDir, "session_id")); err == nil {
			captured = strings.TrimSpace(string(b))
		}
	}
	if captured == "" {
		return
	}
	entry.mu.Lock()
	entry.result.SessionID = captured
	entry.mu.Unlock()
}

// sessionReCache 缓存编译后的 SessionCapture 正则（同一 agent 的正则在每个 job 终态
// 都会用到），避免重复编译。键是正则源串。
var sessionReCache sync.Map // map[string]*regexp.Regexp

// captureSessionID 从 path 文件内容用正则 reSrc 提取 session_id（第 1 个捕获组）。
// 任何失败（正则非法、文件读不到、无匹配、无捕获组）都返回 ""——纯 best-effort，调用方
// 据空判定回退。文件读取受 maxResultJSONBytes 量级约束（stdout 可能很大，仅取必要前缀
// 仍可靠：会话 id 在 codex 输出头部）。
func captureSessionID(path, reSrc string) string {
	if path == "" || reSrc == "" {
		return ""
	}
	re := compileSessionRe(reSrc)
	if re == nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	m := re.FindSubmatch(b)
	if len(m) < 2 { // 需要至少 1 个捕获组。
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// CaptureSessionIDBytes extracts the first capture group from b using the same
// cached regex path as terminal log capture. It is exported for PTY relay output
// observation, where interactive agent output does not enter stdout/stderr logs.
func CaptureSessionIDBytes(b []byte, reSrc string) string {
	if len(b) == 0 || reSrc == "" {
		return ""
	}
	re := compileSessionRe(reSrc)
	if re == nil {
		return ""
	}
	m := re.FindSubmatch(b)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// compileSessionRe 返回 reSrc 编译后的正则（带缓存）；非法正则返回 nil（记一次 warning）。
func compileSessionRe(reSrc string) *regexp.Regexp {
	if v, ok := sessionReCache.Load(reSrc); ok {
		if re, _ := v.(*regexp.Regexp); re != nil {
			return re
		}
		return nil // 之前已判定非法（存了 nil）。
	}
	re, err := regexp.Compile(reSrc)
	if err != nil {
		slog.Warn("captureOutcomes: invalid session_capture regex, ignored", "regex", reSrc, "err", err)
		sessionReCache.Store(reSrc, (*regexp.Regexp)(nil))
		return nil
	}
	sessionReCache.Store(reSrc, re)
	return re
}

// applyOutcome 把远端 runner 回传的 Outcome 落到 entry.result（P4）。它只写非空字段
// （远端某项缺省时不覆盖），并把 Source 一并写入以标注执行来源（worker:/peer:）。
// Artifacts 是远端已序列化好的 []ArtifactItem 清单 JSON，验证后原样写入 ArtifactsJSON
// （非法 JSON 跳过，避免污染 DB / 后续 manifestFor 解析失败）。
func (s *Service) applyOutcome(entry *jobEntry, o *runner.Outcome) {
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if o.RenderedCommand != "" {
		entry.result.RenderedCommand = o.RenderedCommand
	}
	if o.ResultJSON != "" {
		entry.result.ResultJSON = o.ResultJSON
	}
	if o.DiffSummary != "" {
		entry.result.DiffSummary = o.DiffSummary
	}
	if len(o.Artifacts) > 0 {
		if json.Valid(o.Artifacts) {
			entry.result.ArtifactsJSON = string(o.Artifacts)
		} else {
			slog.Warn("applyOutcome: remote artifacts manifest is not valid JSON, skipped", "job_id", entry.result.ID)
		}
	}
	if o.Source != "" {
		entry.result.Source = o.Source
	}
	// 远端(worker/peer)本地捕获/注入的 session_id 回传落库（P3），让远端执行的 job 也能
	// resume / list --session。空=远端未捕获，不覆盖既有值（与其他字段同语义）。
	if o.SessionID != "" {
		entry.result.SessionID = o.SessionID
	}
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

// goferJobEnv 在 agent-config env 之上注入 gofer 自有的 job 元数据环境变量
// （GOFER_RESULT_DIR / GOFER_CWD / GOFER_JOB_ID），返回新 map（不改入参）。
// 这些是 exec 类 job 定位自身 result_dir 的唯一通道（exec argv 逐字执行、不做
// {{result_dir}} 模板替换），从而让「写 <result_dir>/result.json（E6）/ artifacts/
// （E1）」的产出约定对 exec/wrapper 也可用；cli-agent 仍可继续用 {{result_dir}}。
// 注入值优先于继承的进程 env（mergedEnv 中 req.Env 覆盖 os.Environ）。
func goferJobEnv(base map[string]string, jobID, cwd, resultDir string) map[string]string {
	env := make(map[string]string, len(base)+3)
	for k, v := range base {
		env[k] = v
	}
	env["GOFER_JOB_ID"] = jobID
	env["GOFER_CWD"] = cwd
	env["GOFER_RESULT_DIR"] = resultDir
	return env
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
		slog.Warn("captureOutcomes: marshal rendered command", "job_id", req.JobID, "err", err)
		return ""
	}
	return string(b)
}

// setRunningRenderedCommand applies a rendered command reported by a remote runner
// (worker/peer) while the job is still running, then persists a snapshot so job
// show/web reflect WHAT is running at once — not only at completion (G1). Idempotent
// via first-non-empty-wins: the terminal Outcome's identical rendered command is
// then a no-op, and a local job's inline value (execute.go) is never overwritten.
// Called from the remote runner's frame goroutine, so it locks the entry.
func (s *Service) setRunningRenderedCommand(entry *jobEntry, jobID, rendered string) {
	if rendered == "" {
		return
	}
	entry.mu.Lock()
	if entry.result.RenderedCommand != "" {
		entry.mu.Unlock()
		return
	}
	entry.result.RenderedCommand = rendered
	snap := entry.result
	entry.mu.Unlock()
	if err := s.persist(snap); err != nil {
		slog.Warn("persist running rendered command", "job_id", jobID, "err", err)
	}
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
		slog.Warn("captureOutcomes: result.json too large, skipped", "path", p, "size", fi.Size(), "max", maxResultJSONBytes)
		return ""
	}
	b, err := os.ReadFile(p)
	if err != nil {
		slog.Warn("captureOutcomes: read result.json", "path", p, "err", err)
		return ""
	}
	if !json.Valid(b) {
		slog.Warn("captureOutcomes: result.json is not valid JSON, skipped", "path", p)
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
		slog.Warn("captureOutcomes: scan artifacts", "base", base, "err", walkErr)
	}
	if truncated {
		slog.Warn("captureOutcomes: artifacts exceed cap, manifest truncated", "base", base, "cap", maxArtifacts)
	}
	return items
}
