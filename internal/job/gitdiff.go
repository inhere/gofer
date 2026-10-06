package job

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/procattr"
)

// E12 diff 快照（P3，design §6.5 / D4）：job 终态时对其 cwd 采集"未提交改动"
// （工作树 vs HEAD/index，即 `git diff`）—— 全量写 <result_dir>/changes.diff，
// `--stat` 摘要入库 DiffSummary。语义为 **未提交的 tracked 改动**：untracked 新
// 文件、agent 自行 commit 的改动 v1 不覆盖（留 v2 的"job 开始打基线 ref"）。
const (
	// diffTimeout 包住整个 captureDiff 的 git 子进程链（探仓 + 全量 + --stat）。
	diffTimeout = 5 * time.Second
	// diffSummaryCap 是 --stat 摘要入库上限（超出截断，避免 DB 列膨胀）。
	diffSummaryCap = 32 * 1024
	// diffFullCap 是全量 diff 写文件上限（超出截断，避免巨型 diff 撑爆磁盘/读取）。
	diffFullCap = 4 * 1024 * 1024
)

// captureDiff 在 cwd 是 git 工作树时采集未提交改动：全量写 <result_dir>/changes.diff
// (0644)，返回 `git diff --stat` 摘要（截断）。非 git 仓 / git 不在 PATH / 超时 /
// 出错一律返回 ""（best-effort，整体优雅降级，绝不 panic、绝不影响 job 终态）。
func captureDiff(cwd, resultDir string) string {
	// No cwd means no checkout to diff: an empty Dir would make git run in the
	// serve process's own directory and diff an unrelated (possibly huge) repo.
	if cwd == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()

	if !isGitWorkTree(ctx, cwd) {
		return ""
	}

	// 一次 git diff 同时返回 stat 摘要和 patch；从输出头部分离摘要，避免对
	// 工作树再做一次全量扫描。patch-with-stat 的格式是 stat、空行、diff --git。
	stat, full := captureGitPatchWithStat(ctx, cwd, "diff")
	if len(full) > 0 && resultDir != "" {
		if err := os.WriteFile(filepath.Join(resultDir, "changes.diff"), full, 0o644); err != nil {
			slog.Warn("captureDiff: write changes.diff", "result_dir", resultDir, "err", err)
		}
	}
	if len(stat) > diffSummaryCap {
		stat = stat[:diffSummaryCap]
	}
	return string(stat)
}

// captureGitPatchWithStat runs one bounded git diff invocation and returns its
// stat prefix plus plain patch body. Callers that need multiple logical ranges
// (such as a managed worktree's committed and uncommitted sections) invoke this
// once per range rather than once for each representation.
func captureGitPatchWithStat(ctx context.Context, cwd string, args ...string) (stat, patch []byte) {
	args = append(args, "--patch-with-stat")
	out := runGit(ctx, cwd, diffFullCap+diffSummaryCap+1, args...)
	stat, patch = splitPatchWithStat(out)
	if len(stat) > diffSummaryCap {
		stat = stat[:diffSummaryCap]
	}
	return stat, patch
}

// splitPatchWithStat separates the stat prefix from the patch returned by
// `git diff --patch-with-stat`. The stat command's output ends with one newline;
// patch-with-stat adds one extra separator newline before the first diff header.
// Keeping the prefix bytes (apart from that separator) makes DiffSummary match
// the standalone `git diff --stat` output while changes.diff remains a plain
// `git diff` patch.
func splitPatchWithStat(out []byte) (stat, patch []byte) {
	if len(out) == 0 {
		return nil, nil
	}
	idx := bytes.Index(out, []byte("diff --git "))
	if idx < 0 {
		return out, nil
	}
	stat = out[:idx]
	for len(stat) >= 2 && stat[len(stat)-1] == '\n' && stat[len(stat)-2] == '\n' {
		stat = stat[:len(stat)-1]
	}
	patch = out[idx:]
	if len(patch) > diffFullCap {
		patch = patch[:diffFullCap]
	}
	return stat, patch
}

// isGitWorkTree 探测 cwd 是否在 git 工作树内（`git rev-parse --is-inside-work-tree`
// 输出 "true"）。git 不在 PATH / cwd 非仓库 / 出错 → false（整体降级为 ""）。
func isGitWorkTree(ctx context.Context, cwd string) bool {
	out := runGit(ctx, cwd, 256, "rev-parse", "--is-inside-work-tree")
	return strings.TrimSpace(string(out)) == "true"
}

// runGit 以 cwd 为工作目录跑一个 git 子进程（口径同 runner/local 的
// exec.CommandContext(ctx,...,Dir=cwd)），stdout 最多读 capBytes 字节后截断。
// 进程出错 / 超时 / git 不在 PATH 时返回已读到的部分（可能为 nil），绝不 panic。
func runGit(ctx context.Context, cwd string, capBytes int, args ...string) []byte {
	cmd := exec.CommandContext(ctx, "git", args...)
	procattr.Background(cmd)
	cmd.Dir = cwd
	// Read-only: never take .git/index.lock (tools-3wc). status/diff otherwise refresh
	// the index under an "optional" lock, so a concurrent user commit/rebase in the
	// same repo fails with "index.lock: File exists", and a child killed by ctx or the
	// output cap can leave the lock behind.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		// git 不在 PATH / 无法启动：优雅降级。
		return nil
	}

	// 限读 capBytes 后截断：超出部分丢弃，但仍要 drain 让子进程不卡在写管道。
	out, _ := io.ReadAll(io.LimitReader(stdout, int64(capBytes)))
	_, _ = io.Copy(io.Discard, stdout)

	// Wait 必须调用以回收子进程；非零退出 / 超时只影响"是否有输出"，不报错给上层。
	_ = cmd.Wait()
	return out
}
