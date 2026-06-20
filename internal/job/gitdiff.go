package job

import (
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()

	if !isGitWorkTree(ctx, cwd) {
		return ""
	}

	// 全量 diff（仅 tracked 改动，D4）：非空才落盘，避免无改动时写空文件。
	full := runGit(ctx, cwd, diffFullCap, "diff")
	if len(full) > 0 && resultDir != "" {
		if err := os.WriteFile(filepath.Join(resultDir, "changes.diff"), full, 0o644); err != nil {
			log.Printf("captureDiff: write changes.diff under %s: %v", resultDir, err)
		}
	}

	// --stat 摘要入库（截断到 diffSummaryCap）。
	return string(runGit(ctx, cwd, diffSummaryCap, "diff", "--stat"))
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
	cmd.Dir = cwd

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
