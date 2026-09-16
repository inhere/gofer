package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
)

// WT-01 受管 worktree：`job run --worktree` 把整个 job 放进该仓库的一个独立 git
// worktree 里执行，提交落在独立分支上，主 checkout 不再被并行的 agent job 互相踩
// （本周三次 .git/index.lock 残留的根因）。设计见
// docs/design/2026-09-16-job-recovery-and-worktree-design.md「二、WT-01」。
//
// 本文件是**执行机侧**（serve 的 local runner 与 worker 的本地 job.Service 共用同一
// 份 Submit 路径）的唯一实现：定位仓库 → 落 worktree → 映射 cwd → 导出 env；job 结束
// 后的两段 diff、终态分支状态，以及 W2 的 ls/rm/retention 清理也都基于这里的 ref。
const (
	// worktreeSubdir 是 worktree 相对仓库顶层的落点：<top>/tmp/gofer/wt/<job-id>。
	// tmp/ 在各项目已被忽略（结果/交换目录同在此处），故 worktree 目录随之被忽略，
	// 不会在主 checkout 的 `git status` 里冒出来。
	worktreeSubdir = "tmp/gofer/wt"
	// worktreeBranchPrefix 是 job 的 worktree 提交所在分支前缀，完整名 gofer/<job-id>。
	worktreeBranchPrefix = "gofer/"
	// worktreeTimeout 包住**创建** worktree 的 git 命令链（worktree add + switch）：
	// 比只读的 diffTimeout 长，因为 add 要 checkout 整棵树。
	worktreeTimeout = 60 * time.Second
	// worktreeDefaultRef 是请求未给 --worktree-base 时的基线 ref：当前 HEAD。
	worktreeDefaultRef = "HEAD"
)

// 导出给 worktree job 的环境变量（值见 worktreeRef）。agent 侧据此知道"我在受管
// worktree 里跑"，例如把产物/日志写回主 checkout 之外，或用 base sha 生成 diff。
const (
	envWorktree       = "GOFER_WORKTREE"
	envWorktreeBranch = "GOFER_WORKTREE_BRANCH"
	envWorktreeBase   = "GOFER_WORKTREE_BASE"
)

// worktreeRef 描述一个受管 worktree（WT-01）：checkout 目录、其提交所在分支、
// 创建时的基线提交。
type worktreeRef struct {
	// Path is the worktree checkout dir: <Top>/tmp/gofer/wt/<job-id>.
	Path string
	// Branch is the branch the job's commits land on: gofer/<job-id>.
	Branch string
	// BaseSHA is the resolved base commit (--worktree-base, default the checkout's
	// HEAD). Resolved to a sha so the diff/commits-ahead accounting survives the ref
	// moving afterwards.
	BaseSHA string
	// Top is the repository toplevel the worktree belongs to.
	Top string
}

// prepareJobWorktree 为请求了 --worktree 的 job 创建受管 worktree 并返回它。
// cwd 是 job 已解析出的 checkout 目录（execute 前的最终 cwd）。
//
// 仓库用 `git rev-parse --show-toplevel` **从 cwd 出发**定位，因此嵌套仓库
// （如 monorepo 里的 tools/gofer）自然命中最近的顶层作为宿主。cwd 不在任何 checkout
// 内、或本机没有 git，都以可执行的错误拒绝，而不是悄悄退回共享 checkout 里跑。
//
// 创建两步：`git -C <top> worktree add --detach <path> <base>` 再
// `git -C <path> switch -c gofer/<job-id>`（设计 D：先 detached 落在 base 上，再生成
// 具名分支，避免 worktree add -b 对既有分支名的隐式依赖）。
func prepareJobWorktree(ctx context.Context, cwd, jobID, base string) (*worktreeRef, error) {
	if strings.TrimSpace(base) == "" {
		base = worktreeDefaultRef
	}
	top, err := gitTopLevel(ctx, cwd)
	if err != nil {
		return nil, err
	}
	baseSHA, err := gitOut(ctx, top, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%w: worktree base %q is not a commit in %s", ErrInvalidRequest, base, top)
	}
	path := filepath.Join(top, filepath.FromSlash(worktreeSubdir), jobID)
	branch := worktreeBranchPrefix + jobID
	if _, err := gitOut(ctx, top, "worktree", "add", "--detach", path, baseSHA); err != nil {
		return nil, fmt.Errorf("worktree add %s: %w", path, err)
	}
	if _, err := gitOut(ctx, path, "switch", "-c", branch); err != nil {
		// Roll the half-created worktree back so a failure leaves no unreferenced
		// checkout (and no stale `git worktree list` entry) behind.
		_, _ = gitOut(ctx, top, "worktree", "remove", "--force", path)
		return nil, fmt.Errorf("worktree switch -c %s: %w", branch, err)
	}
	return &worktreeRef{Path: path, Branch: branch, BaseSHA: baseSHA, Top: top}, nil
}

// mapCwd 把 job 的 checkout cwd 按**相同相对子路径**映射进 worktree：`--cwd sub`
// 在 <wt>/sub 里跑，默认的 "." 即 worktree 根。子路径在 checkout 里不存在时按需创建
// （父 checkout 里只作为未跟踪目录存在、因而不在任何提交里的 cwd，在新的 worktree 里
// 本来就不存在）。
func (w *worktreeRef) mapCwd(cwd string) (string, error) {
	rel, err := filepath.Rel(w.Top, cwd)
	if err != nil {
		return "", fmt.Errorf("worktree: map cwd %s: %w", cwd, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: cwd %s is outside the git checkout %s", ErrInvalidRequest, cwd, w.Top)
	}
	mapped := filepath.Join(w.Path, rel)
	if err := os.MkdirAll(mapped, 0o755); err != nil {
		return "", fmt.Errorf("worktree: create mapped cwd: %w", err)
	}
	return mapped, nil
}

// worktreeEnv 在 gofer job 元数据 env 之上补三个 worktree 变量（设计 §二：env
// GOFER_WORKTREE / GOFER_WORKTREE_BRANCH / GOFER_WORKTREE_BASE=base sha），返回新 map
// （不改入参），与 goferJobEnv 同口径：注入值优先于继承的进程 env。
func (w *worktreeRef) worktreeEnv(base map[string]string) map[string]string {
	env := make(map[string]string, len(base)+3)
	for k, v := range base {
		env[k] = v
	}
	env[envWorktree] = w.Path
	env[envWorktreeBranch] = w.Branch
	env[envWorktreeBase] = w.BaseSHA
	return env
}

// worktreeState 探测 worktree 当前的分支状态：HEAD sha、相对 base 的提交数、是否有
// 未提交改动（含未跟踪文件）。read-only 探测，任一失败按零值返回（best-effort，与
// captureDiff 同口径：绝不因 git 的问题影响 job 终态）。
func (w *worktreeRef) worktreeState(ctx context.Context) (headSHA string, commitsAhead int, dirty bool) {
	headSHA = strings.TrimSpace(string(runGit(ctx, w.Path, 256, "rev-parse", "HEAD")))
	if n, err := strconv.Atoi(strings.TrimSpace(string(runGit(ctx, w.Path, 64, "rev-list", "--count", w.BaseSHA+"..HEAD")))); err == nil {
		commitsAhead = n
	}
	dirty = len(bytes.TrimSpace(runGit(ctx, w.Path, 4096, "status", "--porcelain"))) > 0
	return headSHA, commitsAhead, dirty
}

// captureWorktreeDiff 是 worktree job 的 E12 diff 采集（设计 §二：`captureDiff` 在
// worktree job 上改为两段）。两段都写进 <result_dir>/changes.diff（分段标题）：
//
//	=== committed (<base>..HEAD) ===  —— 分支上已提交的交付物（普通 job 的
//	                                     captureDiff 覆盖不到的那部分）
//	=== uncommitted ===               —— 结束时仍在工作区的改动
//
// 返回同样分段的 `--stat` 摘要入库（DiffSummary），无任何改动时返回 ""（与 captureDiff
// 一致：非空才落盘）。best-effort：git 不可用/超时/出错只影响摘要，绝不影响终态。
func captureWorktreeDiff(w *worktreeRef, resultDir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()

	committed := runGit(ctx, w.Path, diffFullCap, "diff", w.BaseSHA+"..HEAD")
	uncommitted := runGit(ctx, w.Path, diffFullCap, "diff")
	if len(committed) == 0 && len(uncommitted) == 0 {
		return ""
	}
	if resultDir != "" {
		body := joinDiffSections(w.BaseSHA, committed, uncommitted)
		if len(body) > diffFullCap {
			body = body[:diffFullCap]
		}
		if err := os.WriteFile(filepath.Join(resultDir, "changes.diff"), body, 0o644); err != nil {
			slog.Warn("captureWorktreeDiff: write changes.diff", "result_dir", resultDir, "err", err)
		}
	}

	statCommitted := runGit(ctx, w.Path, diffSummaryCap, "diff", "--stat", w.BaseSHA+"..HEAD")
	statUncommitted := runGit(ctx, w.Path, diffSummaryCap, "diff", "--stat")
	if len(statCommitted) == 0 && len(statUncommitted) == 0 {
		return ""
	}
	summary := joinDiffSections(w.BaseSHA, statCommitted, statUncommitted)
	if len(summary) > diffSummaryCap {
		summary = summary[:diffSummaryCap]
	}
	return string(summary)
}

// joinDiffSections 把两段 diff（或 --stat）拼成分段标题的正文。空段只留标题之外的内容
// 由调用方裁剪，这里空段直接省略标题，避免"有改动却只看到空标题"。
func joinDiffSections(baseSHA string, committed, uncommitted []byte) []byte {
	var b bytes.Buffer
	if len(committed) > 0 {
		fmt.Fprintf(&b, "=== committed (%s..HEAD) ===\n", shortSHA(baseSHA))
		b.Write(committed)
	}
	if len(uncommitted) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("=== uncommitted ===\n")
		b.Write(uncommitted)
	}
	return b.Bytes()
}

// shortSHA 截断 sha 供分段标题显示（7 位，git 的惯例缩写长度）；短 sha 原样返回。
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// worktreeRequested 解析请求是否走受管 worktree：显式 --worktree，或项目级
// worktree_default: true（设计 §二）。在提交时**就**把默认值落到请求上，因此 Forward、
// request_json 与执行机的行为都基于同一个显式决定，执行机不需要再读自己的项目默认值。
func worktreeRequested(cfg *config.Config, req *JobRequest) bool {
	if req.Worktree {
		return true
	}
	return cfg.Projects[req.ProjectKey].WorktreeDefault
}

// gitTopLevel 用 `git rev-parse --show-toplevel` 定位 cwd 所在的仓库顶层。git 不在
// PATH 与"cwd 不是 git checkout"是两类不同的失败，分别给出可执行的错误（设计 §二：
// 非 git → 拒绝 `worktree requires a git checkout`；git 缺失 → 明确错误）。
func gitTopLevel(ctx context.Context, cwd string) (string, error) {
	out, err := gitOut(ctx, cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%w: worktree requires git on PATH", ErrInvalidRequest)
		}
		return "", fmt.Errorf("%w: worktree requires a git checkout", ErrInvalidRequest)
	}
	top := strings.TrimSpace(out)
	if top == "" {
		return "", fmt.Errorf("%w: worktree requires a git checkout", ErrInvalidRequest)
	}
	return filepath.Clean(top), nil
}

// gitOut 跑一个 git 子进程并返回其 stdout（已 TrimSpace）。与 gitdiff.go 的 runGit
// 不同，它**返回错误**：worktree 的创建/清理是必须成功的动作，失败必须让调用方看见，
// 而 runGit 是只读探测的优雅降级口径。stderr 原文并入错误信息，便于定位（如
// "fatal: '<path>' already exists"）。exec.ErrNotFound 原样透传，让上层区分"没有
// git"与"不是仓库"。
//
// GIT_OPTIONAL_LOCKS=0 同 runGit：不因 refresh 去抢 .git/index.lock，避免并行 agent
// job 之间以及与用户的手工 commit/rebase 互相阻塞（tools-3wc）。
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", err
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}
