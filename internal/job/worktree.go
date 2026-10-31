package job

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/inhere/gofer/internal/delivery"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/jobstore"
	"github.com/inhere/gofer/internal/procattr"
	"github.com/inhere/gofer/internal/util"
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
	// worktreeSubdir is the default worktree parent used by tests.
	// The repository-local location is ignored in the common git exclude file.
	// 不会在主 checkout 的 `git status` 里冒出来。
	worktreeSubdir = ".worktrees"
	// legacyWorktreeSubdir is the former default, retained for stale-checkout cleanup.
	legacyWorktreeSubdir = "tmp/gofer/wt"
	// worktreeBranchPrefix 是 job 的 worktree 提交所在分支前缀，完整名 gofer/<job-id>。
	worktreeBranchPrefix = "gofer/"
	// worktreeTimeout 包住**创建** worktree 的 git 命令链（worktree add + switch）：
	// 比只读的 diffTimeout 长，因为 add 要 checkout 整棵树。
	worktreeTimeout = 60 * time.Second
	// worktreeDefaultRef 是请求未给 --worktree-base 时的基线 ref：当前 HEAD。
	worktreeDefaultRef = "HEAD"
	// tempCheckoutAttempts / tempCheckoutRetryDelay 是 `ref:` 临时检出的删除重试：
	// Windows 在作业进程退出后仍可能短暂持有目录里的句柄，第一次 `worktree remove`
	// 失败往往几百毫秒后就能成功（曾因此把目录连同 git 登记一起留给了下次清扫）。
	tempCheckoutAttempts   = 3
	tempCheckoutRetryDelay = 300 * time.Millisecond
	// tempCheckoutShutdownTimeout 是停机期间删临时检出的预算：进程要尽快退出，删不掉的
	// 留给下次启动的清扫，绝不为了它拖住 shutdown。
	tempCheckoutShutdownTimeout = 15 * time.Second
	// worktreeAddAttempts / worktreeAddRetryBase 是 `git worktree add` 对「同一仓库正被
	// 别的进程建 worktree」这类瞬时错误的退避重试（gofer-ourk）：主机与容器同时往一个仓库
	// 建 worktree 时，git 扫 .git/worktrees/* 会读到刚建了一半的登记而直接 fatal。窗口是毫秒
	// 级，最坏多等 200+400+800ms，仍远在 worktreeTimeout 之内。
	worktreeAddAttempts  = 4
	worktreeAddRetryBase = 200 * time.Millisecond
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
	// Path is the worktree checkout dir: <Top>/.worktrees/<name>.
	Path string
	// Branch is the branch the job's commits land on: gofer/<job-id>.
	Branch string
	// BaseSHA is the resolved base commit (--worktree-base, default the checkout's
	// HEAD). Resolved to a sha so the diff/commits-ahead accounting survives the ref
	// moving afterwards.
	BaseSHA string
	// Top is the repository toplevel the worktree belongs to.
	Top string
	// Links are the project-relative dependency links this worktree carries (see
	// linkWorktreeDeps); removal unlinks them before the checkout goes away.
	Links []string
	// Temp marks the checkout of a `ref:` job (design 一·3): detached at BaseSHA, on NO
	// branch, and removed the moment the job ends (see cleanupTempWorktree).
	Temp bool
}

// prepareJobWorktree 为请求了 --worktree 的 job 创建受管 worktree 并返回它。
// cwd 是 job 已解析出的 checkout 目录（execute 前的最终 cwd）。opts 是提交侧已解析好的
// 位置/分支/链接决定（worktreeOptionsFor）。
//
// 仓库用 `git rev-parse --show-toplevel` **从 cwd 出发**定位，因此嵌套仓库
// （如 monorepo 里的 tools/gofer）自然命中最近的顶层作为宿主。cwd 不在任何 checkout
// 内、或本机没有 git，都以可执行的错误拒绝，而不是悄悄退回共享 checkout 里跑。
//
// 创建两步：`git -C <top> worktree add --detach <path> <base>` 再
// `git -C <path> switch -c gofer/<job-id>`（设计 D：先 detached 落在 base 上，再生成
// 具名分支，避免 worktree add -b 对既有分支名的隐式依赖）。
func prepareJobWorktree(ctx context.Context, cwd, jobID, base string, opts worktreeCreateOptions) (*worktreeRef, error) {
	if strings.TrimSpace(base) == "" {
		base = worktreeDefaultRef
	}
	if opts.Dir == "" {
		opts.Dir = config.DefaultWorktreeDir
	}
	if opts.Branch == "" {
		opts.Branch = config.DefaultWorktreeBranch
	}
	top, err := gitTopLevel(ctx, cwd)
	if err != nil {
		return nil, err
	}
	baseSHA, err := gitOut(ctx, top, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%w: worktree base %q is not a commit in %s", ErrInvalidRequest, base, top)
	}
	// Keep generated worktrees out of the user's index without modifying tracked config.
	ensureWorktreeIgnored(ctx, top, opts.Dir)
	path := filepath.Join(top, filepath.FromSlash(opts.Dir))
	branch := opts.Branch
	// A same-named checkout or branch is REFUSED, never reused: two jobs sharing one
	// worktree would fight over the same index and branch, which is the failure
	// --worktree exists to prevent.
	if isDir(path) {
		return nil, fmt.Errorf("%w: worktree dir %s already exists (pick another --worktree-name)", ErrInvalidRequest, path)
	}
	if _, err := gitOut(ctx, top, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return nil, fmt.Errorf("%w: branch %s already exists (pick another --branch or --worktree-name)", ErrInvalidRequest, branch)
	}
	if err := gitWorktreeAdd(ctx, top, "worktree", "add", "--detach", path, baseSHA); err != nil {
		return nil, fmt.Errorf("worktree add %s: %w", path, err)
	}
	if _, err := gitOut(ctx, path, "switch", "-c", branch); err != nil {
		// Roll the half-created worktree back so a failure leaves no unreferenced
		// checkout (and no stale `git worktree list` entry) behind.
		_, _ = gitOut(ctx, top, "worktree", "remove", "--force", path)
		return nil, fmt.Errorf("worktree switch -c %s: %w", branch, err)
	}
	return &worktreeRef{Path: path, Branch: branch, BaseSHA: baseSHA, Top: top, Links: opts.Links}, nil
}

// prepareTempWorktree creates the TEMPORARY checkout a `ref:` job runs in (design 一·3):
// <top>/.worktrees/<name> detached at commit sha, with NO branch, so nothing of it
// can be merged, pushed or kept. It is the same落点 and the same dependency links as a
// managed worktree (a `ref:` verify needs the project's node_modules just as much);
// only the branch and the lifetime differ — cleanupTempWorktree removes it when the job
// ends.
//
// sha must already be in the checkout at cwd (fetchCommit gets it there);
// `worktree add --detach` can only check out an object this repository has.
func prepareTempWorktree(ctx context.Context, cwd, jobID, sha string, opts worktreeCreateOptions) (*worktreeRef, error) {
	if strings.TrimSpace(sha) == "" {
		return nil, fmt.Errorf("%w: ref checkout needs a commit", ErrInvalidRequest)
	}
	if opts.Dir == "" {
		opts.Dir = config.DefaultWorktreeDir
	}
	top, err := gitTopLevel(ctx, cwd)
	if err != nil {
		return nil, err
	}
	resolved, err := gitOut(ctx, top, "rev-parse", "--verify", sha+"^{commit}")
	if err != nil {
		return nil, fmt.Errorf("%w: ref commit %q is not in %s", ErrInvalidRequest, sha, top)
	}
	sha = resolved
	ensureWorktreeIgnored(ctx, top, opts.Dir)
	path := filepath.Join(top, filepath.FromSlash(opts.Dir))
	if isDir(path) {
		return nil, fmt.Errorf("%w: worktree dir %s already exists", ErrInvalidRequest, path)
	}
	if err := gitWorktreeAdd(ctx, top, "worktree", "add", "--detach", path, sha); err != nil {
		return nil, fmt.Errorf("worktree add --detach %s: %w", path, err)
	}
	return &worktreeRef{Path: path, BaseSHA: sha, Top: top, Links: opts.Links, Temp: true}, nil
}

// ResolveCommitRef resolves a ref — full or abbreviated sha, tag, branch, HEAD~N — to the
// full commit sha of the checkout at dir. The workflow resolves its `ref:` ONCE at submit
// with it, so every fan of the step, on every runner, checks out the SAME tree even if a
// branch moves while the workflow runs (design 一·3).
func ResolveCommitRef(ctx context.Context, dir, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("%w: empty ref", ErrInvalidRequest)
	}
	sha, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil || sha == "" {
		return "", fmt.Errorf("%w: ref %q is not a commit in %s", ErrInvalidRequest, ref, dir)
	}
	return sha, nil
}

// fetchCommit makes sure commit is present in the checkout at dir, fetching it from the
// repository's default remote when it is not (design 一·3, D3): the hub resolved the sha,
// and the machine that runs the job may be a worker whose clone never saw that commit
// yet. Two attempts, most specific first — `git fetch <remote> <sha>` (modern git servers
// allow fetching a reachable sha) and then a plain `git fetch <remote>`, which picks the
// commit up as soon as it is reachable from any branch or tag. Neither touches the
// working tree or the current branch.
//
// When both fail the job fails with the actionable reason instead of silently checking
// out something else: the commit has not been pushed (D3: the worker only ever sees
// commits the remote already has).
func fetchCommit(ctx context.Context, dir, sha string) error {
	if commitPresent(ctx, dir, sha) {
		return nil
	}
	remote := defaultRemoteName(ctx, dir)
	_, firstErr := gitOut(ctx, dir, "fetch", "--no-tags", remote, sha)
	if !commitPresent(ctx, dir, sha) {
		if _, err := gitOut(ctx, dir, "fetch", "--no-tags", remote); err != nil {
			firstErr = err
		}
	}
	if commitPresent(ctx, dir, sha) {
		return nil
	}
	reason := ""
	if firstErr != nil {
		reason = ": " + firstErr.Error()
	}
	return fmt.Errorf("%w: commit %s 不在远端 %q，先推送该提交，或改在 server 本机检查%s", ErrInvalidRequest, sha, remote, reason)
}

// commitPresent reports whether the checkout at dir has the commit object sha.
func commitPresent(ctx context.Context, dir, sha string) bool {
	if strings.TrimSpace(sha) == "" {
		return false
	}
	_, err := gitOut(ctx, dir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// defaultRemoteName returns the remote to fetch a missing commit from: "origin" when the
// repository has it, else its first remote, else "origin" (the git default, so the
// failure message names something the operator recognises).
func defaultRemoteName(ctx context.Context, dir string) string {
	out, err := gitOut(ctx, dir, "remote")
	if err != nil {
		return "origin"
	}
	remotes := strings.Fields(out)
	for _, r := range remotes {
		if r == "origin" {
			return r
		}
	}
	if len(remotes) > 0 {
		return remotes[0]
	}
	return "origin"
}

// mapCwd 把 job 的 checkout cwd 按**相同相对子路径**映射进 worktree：`--cwd sub`
// 在 <wt>/sub 里跑，默认的 "." 即 worktree 根。子路径在 checkout 里不存在时按需创建
// （父 checkout 里只作为未跟踪目录存在、因而不在任何提交里的 cwd，在新的 worktree 里
// 本来就不存在）。
func (w *worktreeRef) mapCwd(cwd string) (string, error) {
	// Compare through symlinks: `git rev-parse --show-toplevel` reports the
	// RESOLVED toplevel, while the caller's cwd is whatever path they typed (on
	// macOS /var/... vs git's /private/var/...). A raw Rel would call a legitimate
	// cwd "outside the checkout" and reject the job.
	rel, err := filepath.Rel(util.RealPath(w.Top), util.RealPath(cwd))
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
	return util.EnvWith(base, map[string]string{
		envWorktree:       w.Path,
		envWorktreeBranch: w.Branch,
		envWorktreeBase:   w.BaseSHA,
	})
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
	return captureWorktreeDiffSince(w, resultDir, nil)
}

// captureWorktreeDiffSince is captureWorktreeDiff for a job whose changes are measured
// from w.BaseSHA (the caller passes a copy of the ref carrying the job's own baseline)
// and which leaves out the uncommitted changes of the excluded paths.
func captureWorktreeDiffSince(w *worktreeRef, resultDir string, exclude []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()

	statCommitted, committed := captureGitPatchWithStat(ctx, w.Path, "diff", w.BaseSHA+"..HEAD")
	statUncommitted, uncommitted := captureGitPatchWithStat(ctx, w.Path, uncommittedDiffArgs(exclude)...)
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

// ensureWorktreeIgnored adds a local-only ignore rule for the conventional .worktrees
// location. Failure is deliberately best-effort: creating the worktree must still proceed.
func ensureWorktreeIgnored(ctx context.Context, top, rel string) {
	clean := filepath.ToSlash(filepath.Clean(rel))
	if clean != ".worktrees" && !strings.HasPrefix(clean, ".worktrees/") {
		return
	}
	if _, err := gitOut(ctx, top, "check-ignore", "-q", "--", clean); err == nil {
		return
	}
	common, err := gitOut(ctx, top, "rev-parse", "--git-common-dir")
	if err != nil {
		slog.Warn("job.worktree_ignore_failed", "event", "job.worktree_ignore_failed", "path", clean, "err", err)
		return
	}
	common = strings.TrimSpace(common)
	if !filepath.IsAbs(common) {
		common = filepath.Join(top, filepath.FromSlash(common))
	}
	exclude := filepath.Join(common, "info", "exclude")
	data, readErr := os.ReadFile(exclude)
	if readErr == nil {
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			if strings.TrimSpace(line) == "/.worktrees/" {
				return
			}
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		slog.Warn("job.worktree_ignore_failed", "event", "job.worktree_ignore_failed", "path", clean, "err", readErr)
		return
	}
	if err := os.MkdirAll(filepath.Dir(exclude), 0o755); err != nil {
		slog.Warn("job.worktree_ignore_failed", "event", "job.worktree_ignore_failed", "path", clean, "err", err)
		return
	}
	f, err := os.OpenFile(exclude, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		slog.Warn("job.worktree_ignore_failed", "event", "job.worktree_ignore_failed", "path", clean, "err", err)
		return
	}
	defer f.Close()
	prefix := ""
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		prefix = "\n"
	}
	if _, err := fmt.Fprintf(f, "%s# Added by gofer for managed worktrees\n/.worktrees/\n", prefix); err != nil {
		slog.Warn("job.worktree_ignore_failed", "event", "job.worktree_ignore_failed", "path", clean, "err", err)
	}
}

// 不同，它**返回错误**：worktree 的创建/清理是必须成功的动作，失败必须让调用方看见，
// 而 runGit 是只读探测的优雅降级口径。stderr 原文并入错误信息，便于定位（如
// "fatal: '<path>' already exists"）。exec.ErrNotFound 原样透传，让上层区分"没有
// git"与"不是仓库"。
//
// GIT_OPTIONAL_LOCKS=0 同 runGit：不因 refresh 去抢 .git/index.lock，避免并行 agent
// job 之间以及与用户的手工 commit/rebase 互相阻塞（tools-3wc）。
//
// LANGUAGE=en keeps git's messages in English whatever the machine locale: the race
// detection (isWorktreeRaceError) and callers match git's English stderr, and a
// zh_CN machine with git translations installed would otherwise answer in Chinese.
// LANGUAGE outranks LC_ALL for messages, so the locale (and its charset) is untouched.
// gitOutEnv is the environment gitOut runs git with (see gitOut).
func gitOutEnv() []string {
	return append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LANGUAGE=en")
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	procattr.Background(cmd)
	cmd.Dir = dir
	cmd.Env = gitOutEnv()
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

// --- 并发建 worktree 的瞬时错误重试（gofer-ourk）----------------------------

// worktreeRaceMarkers are the git messages that mean "another process is writing its own
// worktree registration in this repository right now" rather than "this checkout is
// broken": the admin entries live under .git/worktrees/<id>/ and git reads them as a
// directory listing, so a half-created sibling makes it die with
// "failed to read .git/worktrees/<id>/commondir: No error" (or .../gitdir). A repository
// index lock held for a moment by a parallel job is the same kind of transient.
var worktreeRaceMarkers = []string{"/commondir", "/gitdir", "index.lock"}

// isWorktreeRaceError reports whether err is one of those transient registration races.
// Anything else (no git, path/branch already exists, base is not a commit, a corrupted
// registration that stays unreadable) is a real error and is never retried.
func isWorktreeRaceError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range worktreeRaceMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// gitWorktreeAdd runs a `git worktree add ...` through gitOut, retrying ONLY the transient
// registration race (isWorktreeRaceError) with a bounded backoff (attempt N waits
// N*worktreeAddRetryBase). Two machines — typically the host and a container — creating
// worktrees of the same repository at the same moment is a normal setup (a fan-out step
// dispatched to both), and the race window is milliseconds. A real error is returned on the
// first attempt unchanged, and so is the race once the attempts are used up: the operator
// still sees git's own message.
func gitWorktreeAdd(ctx context.Context, dir string, args ...string) error {
	return retryWorktreeAdd(ctx, func() error {
		_, err := gitOut(ctx, dir, args...)
		return err
	})
}

// retryWorktreeAdd is gitWorktreeAdd's retry loop without the git call, so the policy (what
// is retried, how many times, how it backs off, when it gives up) is testable on its own.
func retryWorktreeAdd(ctx context.Context, run func() error) error {
	var err error
	for attempt := 1; attempt <= worktreeAddAttempts; attempt++ {
		if err = run(); err == nil {
			return nil
		}
		if attempt == worktreeAddAttempts || !isWorktreeRaceError(err) {
			return err
		}
		delay := time.Duration(attempt) * worktreeAddRetryBase
		slog.Warn("worktree add lost a race with another worktree registration, retrying",
			"attempt", attempt, "backoff", delay.String(), "err", err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
	return err
}

// --- ls / rm（W2）----------------------------------------------------------

// WorktreeStatus is the LIVE state of one job's managed worktree (WT-01), as the
// `job worktree ls/rm` commands and GET /v1/jobs/{id}/worktree report it. The
// path/branch/base come from the JOB ROW (recorded at submit) while head/commits/
// dirty/merged are probed from the checkout on the machine serving the request —
// so a row whose worktree was already removed (or whose path lives on another
// machine, e.g. a worker job) reports Exists=false and carries no live state.
type WorktreeStatus struct {
	JobID   string `json:"job_id"`
	Project string `json:"project,omitempty"`
	Path    string `json:"path,omitempty"`
	Branch  string `json:"branch,omitempty"`
	BaseSHA string `json:"base_sha,omitempty"`
	// HeadSHA/CommitsAhead are probed live (and fall back to the recorded terminal
	// values when the worktree is gone), so `ls` answers "did this branch deliver
	// anything, and is it still there?".
	HeadSHA      string `json:"head_sha,omitempty"`
	CommitsAhead int    `json:"commits_ahead"`
	// Dirty reports uncommitted changes (tracked or untracked) in the worktree:
	// removing it would lose them, so `rm` refuses without --force.
	Dirty bool `json:"dirty"`
	// Merged reports that the branch's commits are contained in the base branch's
	// current tip, i.e. nothing is lost by deleting the branch (see worktreeMerged).
	Merged bool `json:"merged"`
	// Exists reports that the worktree directory is present on THIS machine.
	Exists bool `json:"exists"`
	// Warnings explain a removal that completed with a leftover on disk (a Windows
	// directory a process still holds open): the branch and the worktree registration are
	// gone as asked, only the directory itself could not be deleted.
	Warnings []string `json:"warnings,omitempty"`
	// Links lists the dependency links the checkout carries, as RECORDED when they were
	// created (not what the project config says now).
	Links []string `json:"links,omitempty"`
}

var (
	// ErrNoManagedWorktree: the job carries no WT-01 managed worktree (nothing to
	// list or remove). The HTTP layer maps it to 404.
	ErrNoManagedWorktree = errors.New("job has no managed worktree")
	// ErrWorktreeDirty: removal refused because the worktree still has uncommitted
	// changes — deleting it would throw away work the job left behind. Re-run with
	// --force to discard it. The HTTP layer maps it to 409.
	ErrWorktreeDirty = errors.New("worktree has uncommitted changes; re-run with --force to discard them")
	// ErrWorktreeGone: the recorded worktree directory is not there anymore (deleted
	// by hand / the job ran on another machine). The row may still be cleaned up by
	// `git worktree prune` on the owning machine.
	ErrWorktreeGone = errors.New("worktree directory is not present on this machine")
	// ErrWorktreeInUse: removal refused because a LIVE job still works in the checkout. A
	// continuation job (auto-resume, todo continue) inherits its source's worktree and
	// keeps running in the same directory, so "the job whose id was given is finished"
	// says nothing about who owns the checkout now; --force must not override this, or
	// the running job loses the tree under its feet. The HTTP layer maps it to 409.
	ErrWorktreeInUse = errors.New("worktree is still used by a running job")
	// ErrWorktreeBusy: git could not remove the checkout and nothing about it could be
	// completed (the directory is locked and still registered), so the branch and the
	// registration are kept together instead of leaving a half state. The HTTP layer maps
	// it to 409 with the underlying reason.
	ErrWorktreeBusy             = errors.New("worktree checkout is locked by another process")
	ErrWorktreeMergeConflict    = delivery.ErrMergeConflict
	ErrWorktreeMainNotReady     = delivery.ErrMainNotReady
	ErrWorktreeIndexLocked      = delivery.ErrIndexLocked
	ErrWorktreeMergeUnsupported = errors.New("worktree merge is supported only for a local runner")
)

// WorktreeMergeError reports a conflict after the main checkout has been restored.
type WorktreeMergeError = delivery.MergeConflictError

// MergeOptions tunes MergeWorktree.
type MergeOptions struct {
	// Squash merges the branch as a single commit instead of a --no-ff merge commit.
	Squash bool
	// CleanupOthers removes (force, with branch) the worktrees of the other fan jobs
	// of the same workflow step/attempt once the merge succeeded.
	CleanupOthers bool
	// TodoDone closes the plan todo this job belongs to once the merge succeeded. A job
	// that belongs to no todo ignores it (reported in MergeResult.Warnings).
	TodoDone bool
	// Note overrides the todo note; empty = `<branch> 合入 <main short sha>`.
	Note string
	// Comment is posted on the job after a successful merge. Author/Kind identify who
	// wrote it and are stamped by the entry layer (never by the caller's body).
	Comment           string
	CommentAuthor     string
	CommentAuthorKind string
	// Cleanup unlinks the worktree's dependency links, removes the checkout and deletes
	// the branch (git branch -d: only a branch that is fully merged, which it is now).
	Cleanup bool
}

// MergeResult is the merge response: the merged worktree plus the siblings that
// were cleaned up (CleanupOthers) and what the closing flags did.
type MergeResult struct {
	WorktreeStatus
	Cleaned []string `json:"cleaned,omitempty"`
	// Base is the main-checkout branch the merge landed on.
	Base string `json:"base,omitempty"`
	// TodoID / TodoDone / TodoNote report the --todo-done outcome; an empty TodoID with
	// TodoDone=false means the flag was ignored because the job belongs to no todo.
	TodoID   string `json:"todo_id,omitempty"`
	TodoDone bool   `json:"todo_done,omitempty"`
	TodoNote string `json:"todo_note,omitempty"`
	// Commented reports that --comment was recorded; Removed that --cleanup removed the
	// checkout (and branch).
	Commented bool `json:"commented,omitempty"`
	Removed   bool `json:"removed,omitempty"`
	// Warnings explain a closing step that was skipped or failed AFTER a successful
	// merge; they never turn the merge itself into a failure.
	Warnings []string `json:"warnings,omitempty"`
}

// MergeWorktree merges a managed local worktree branch into the project's main
// checkout. The main checkout must be clean and attached to a named branch. A
// conflict is fully aborted before the typed error is returned; no push occurs.
func (s *Service) MergeWorktree(jobID string, opts MergeOptions) (MergeResult, error) {
	res, ok := s.Get(jobID)
	if !ok {
		return MergeResult{}, ErrJobNotFound
	}
	if res.WorktreePath == "" {
		return MergeResult{}, fmt.Errorf("%w: job %s", ErrNoManagedWorktree, jobID)
	}
	if res.Runner != "" && IsRemoteRunner(s.config(), res.Runner) {
		return MergeResult{}, fmt.Errorf("%w (job %s ran on runner %q)", ErrWorktreeMergeUnsupported, jobID, res.Runner)
	}
	st, err := s.WorktreeStatus(jobID)
	if err != nil {
		return MergeResult{}, err
	}
	out := MergeResult{WorktreeStatus: st}
	if !st.Exists {
		return out, ErrWorktreeGone
	}
	// The merge may legitimately wait out a lock (up to indexLockWait), so the budget is
	// the worktree timeout plus that wait rather than the worktree timeout alone.
	ctx, cancel := context.WithTimeout(context.Background(), 2*worktreeTimeout)
	defer cancel()
	main, base, err := delivery.MergeBranch(ctx, st.Path, st.Branch, opts.Squash)
	if err != nil {
		return out, err
	}
	out.Merged, out.Base = true, base
	// Closing steps run AFTER a successful merge only, in this order; each is reported and
	// a failure is a warning — the branch is merged, that part cannot be undone by them.
	if opts.TodoDone {
		s.markMergeTodoDone(ctx, res, st.Branch, main, opts.Note, &out)
	}
	if body := strings.TrimSpace(opts.Comment); body != "" {
		author, kind := opts.CommentAuthor, opts.CommentAuthorKind
		if author == "" {
			author, kind = "gofer", jobstore.CommentAuthorSystem
		}
		if _, _, cerr := s.Comment("job", jobID, author, kind, body); cerr != nil {
			out.Warnings = append(out.Warnings, "comment not recorded: "+cerr.Error())
		} else {
			out.Commented = true
		}
	}
	if opts.Cleanup {
		if rerr := s.cleanupMergedWorktree(ctx, res, st); rerr != nil {
			out.Warnings = append(out.Warnings, "cleanup failed: "+rerr.Error())
		} else {
			out.Removed, out.Exists = true, false
		}
	}
	if opts.CleanupOthers {
		out.Cleaned = s.cleanupSiblingWorktrees(res)
	}
	return out, nil
}

// markMergeTodoDone closes the job's plan todo after a merge, with the note the operator
// gave or `<branch> 合入 <main short sha>`. A job with no todo is not an error: it is
// reported as a warning ("--todo-done ignored").
func (s *Service) markMergeTodoDone(ctx context.Context, res JobResult, branch, main, note string, out *MergeResult) {
	if res.TodoID == "" {
		out.Warnings = append(out.Warnings, "job belongs to no plan todo; --todo-done ignored")
		return
	}
	if strings.TrimSpace(note) == "" {
		sha := ""
		if v, err := gitOut(ctx, main, "rev-parse", "--short", "HEAD"); err == nil {
			sha = v
		}
		note = strings.TrimSpace(branch + " 合入 " + sha)
	}
	if s.meta == nil {
		out.Warnings = append(out.Warnings, "--todo-done: no metadata store")
		return
	}
	ok, err := s.meta.UpdateTodoStatus(res.TodoID, jobstore.TodoDone, &note)
	if err != nil || !ok {
		reason := "todo not found"
		if err != nil {
			reason = err.Error()
		}
		out.Warnings = append(out.Warnings, "--todo-done: "+reason)
		return
	}
	out.TodoID, out.TodoDone, out.TodoNote = res.TodoID, true, note
	s.PlanTodoChanged(res.TodoID, jobstore.TodoDone, res.CallerID)
}

// cleanupMergedWorktree is --cleanup: unlink the dependency links, remove the checkout
// and delete the branch, which is now contained in the main branch (git branch -d
// refuses anything else, so a mistaken cleanup cannot drop unmerged work). The row's
// worktree_path is cleared so `worktree ls` stops advertising a checkout that is gone.
func (s *Service) cleanupMergedWorktree(ctx context.Context, res JobResult, st WorktreeStatus) error {
	projCfg := s.config().Projects[st.Project]
	wt := &worktreeRef{Path: st.Path, Branch: st.Branch, BaseSHA: st.BaseSHA,
		Links: mergeWorktreeLinks(st.Links, projCfg.WorktreeLinks())}
	if err := removeWorktreeMerged(ctx, wt); err != nil {
		return err
	}
	if r, ok := s.Get(res.ID); ok {
		r.WorktreePath = ""
		if perr := s.persist(r); perr != nil {
			slog.Warn("merge cleanup: clear worktree_path", "job_id", res.ID, "err", perr)
		}
	}
	slog.Info("job.worktree_removed", "event", "job.worktree_removed", "component", "job",
		"job_id", res.ID, "path", st.Path, "branch", st.Branch, "reason", "merged by worktree merge")
	return nil
}

// cleanupSiblingWorktrees force-removes the worktrees (and branches) of the other
// fan jobs of the same workflow step generation. Best-effort: a failure is logged
// and the merge result stays successful.
func (s *Service) cleanupSiblingWorktrees(picked JobResult) []string {
	if picked.WorkflowID == "" || s.meta == nil {
		return nil
	}
	recs, err := s.meta.ListWorkflowJobs(picked.WorkflowID)
	if err != nil {
		slog.Warn("merge cleanup: list workflow jobs", "workflow_id", picked.WorkflowID, "err", err)
		return nil
	}
	var cleaned []string
	for _, r := range recs {
		if r.ID == picked.ID || r.StepIndex != picked.StepIndex || r.Attempt != picked.Attempt || r.WorktreePath == "" {
			continue
		}
		if _, err := s.RemoveWorktree(r.ID, true, true); err != nil {
			slog.Warn("merge cleanup: remove sibling worktree", "job_id", r.ID, "err", err)
			continue
		}
		cleaned = append(cleaned, r.ID)
	}
	return cleaned
}

// WorktreeStatus returns the live state of the job's managed worktree. It is a
// read-only probe: git failures downgrade to "not a live worktree" (Exists=false)
// rather than erroring, so `job worktree ls` still lists the branch a job
// delivered even when its checkout is gone.
func (s *Service) WorktreeStatus(jobID string) (WorktreeStatus, error) {
	res, ok := s.Get(jobID)
	if !ok {
		return WorktreeStatus{}, ErrJobNotFound
	}
	if res.WorktreePath == "" {
		return WorktreeStatus{}, fmt.Errorf("%w: job %s", ErrNoManagedWorktree, jobID)
	}
	st := WorktreeStatus{
		JobID:        res.ID,
		Project:      res.ProjectKey,
		Path:         res.WorktreePath,
		Branch:       res.WorktreeBranch,
		BaseSHA:      res.WorktreeBaseSHA,
		HeadSHA:      res.WorktreeHeadSHA,
		CommitsAhead: res.CommitsAhead,
		Links:        res.WorktreeLinks,
	}
	wt := &worktreeRef{Path: res.WorktreePath, Branch: res.WorktreeBranch, BaseSHA: res.WorktreeBaseSHA}
	if !isDir(res.WorktreePath) {
		return st, nil // recorded, but no checkout here: Exists stays false
	}
	ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
	defer cancel()
	st.Exists = true
	if head, ahead, dirty := wt.worktreeState(ctx); head != "" {
		st.HeadSHA, st.CommitsAhead, st.Dirty = head, ahead, dirty
	}
	st.Merged = worktreeMerged(ctx, wt, st.CommitsAhead)
	return st, nil
}

// RemoveWorktree removes a job's managed worktree (WT-01 `job worktree rm`). It
// refuses when the worktree still has uncommitted changes unless force is set, and
// optionally deletes the branch (the deliverable — hence opt-in). The job row's
// worktree_path is cleared on success so `ls` stops advertising a checkout that is
// gone; branch/base/head stay for audit.
//
// A checkout a LIVE job still works in is refused regardless of force: a continuation
// job continues in its source's worktree, so removing it would delete the tree the
// running job is editing. When the directory itself cannot be deleted (Windows: a
// process still holds it) but the branch and the registration can, the removal
// completes and reports a warning naming the leftover path instead of failing.
func (s *Service) RemoveWorktree(jobID string, force, deleteBranch bool) (WorktreeStatus, error) {
	st, err := s.WorktreeStatus(jobID)
	if err != nil {
		return WorktreeStatus{}, err
	}
	if !st.Exists {
		return st, fmt.Errorf("%w: %s", ErrWorktreeGone, st.Path)
	}
	if users := s.worktreeUsers(st.Path); len(users) > 0 {
		return st, fmt.Errorf("%w: %s is used by %s; wait for those jobs to finish (--force does not override this)",
			ErrWorktreeInUse, st.Path, strings.Join(users, ", "))
	}
	if st.Dirty && !force {
		return st, fmt.Errorf("%w: %s", ErrWorktreeDirty, st.Path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), worktreeTimeout)
	defer cancel()
	projCfg := s.config().Projects[st.Project]
	// Unlink what the checkout ACTUALLY carries (recorded at create time) plus whatever
	// the project config lists NOW — a config entry dropped after creation is still a
	// link on disk, and one added since is harmless to try. Removal deletes the link
	// itself, never what it points at.
	wt := &worktreeRef{Path: st.Path, Branch: st.Branch, BaseSHA: st.BaseSHA,
		Links: mergeWorktreeLinks(st.Links, projCfg.WorktreeLinks())}
	warnings, err := removeWorktreeWithWarnings(ctx, wt, force, deleteBranch)
	if err != nil {
		return st, err
	}
	st.Exists = false
	st.Warnings = warnings

	// The row is the index of record: drop the path so a later `ls` does not report a
	// checkout that no longer exists. Best-effort — the removal already happened, and
	// a write failure only leaves a stale path (which `ls` reports as Exists=false).
	if res, ok := s.Get(jobID); ok {
		res.WorktreePath = ""
		if perr := s.persist(res); perr != nil {
			slog.Warn("remove worktree: clear worktree_path", "job_id", jobID, "err", perr)
		}
	}
	slog.Info("job.worktree_removed", "event", "job.worktree_removed", "component", "job",
		"job_id", jobID, "path", st.Path, "branch", st.Branch, "delete_branch", deleteBranch)
	return st, nil
}

// removeWorktree runs `git worktree remove` for wt and, optionally, deletes the
// branch. It runs from the repository's MAIN checkout (derived from the worktree's
// own git dir) so it works regardless of the process's cwd. The warnings of
// removeWorktreeWithWarnings are dropped: every caller of this shape either has no
// operator to report to (retention, a temp checkout) or reports its own outcome.
func removeWorktree(ctx context.Context, wt *worktreeRef, force, deleteBranch bool) error {
	_, err := removeWorktreeWithWarnings(ctx, wt, force, deleteBranch)
	return err
}

// removeWorktreeWithWarnings is removeWorktree plus the leftovers it could not delete:
// an empty warning list means the checkout and the branch are fully gone, a non-empty
// one means everything the caller asked for was done except the directory (see
// finishCheckoutRemoval).
func removeWorktreeWithWarnings(ctx context.Context, wt *worktreeRef, force, deleteBranch bool) ([]string, error) {
	main, err := mainCheckout(ctx, wt.Path)
	if err != nil {
		return nil, err
	}
	// Unlink the dependency links FIRST, one by one, and only the links: this is the
	// step that keeps a cleanup from reaching into the main checkout's tree through a
	// junction/symlink. Done before `worktree remove` so no recursive delete ever
	// meets a link at all.
	removeWorktreeLinks(wt.Path, wt.Links)
	// Belt and braces: whatever the list above missed (a link recorded by an older row,
	// one created by hand, a nested one on a known path), any link still sitting at the
	// worktree's top level or on the way to a known path is unlinked NOW — again only
	// the link itself — so `worktree remove --force` never walks into the main tree.
	sweepWorktreeLinks(wt.Path, wt.Links)
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	var warnings []string
	if _, err := gitOut(ctx, main, append(args, wt.Path)...); err != nil {
		warnings, err = finishCheckoutRemoval(ctx, main, wt, force, err)
		if err != nil {
			return nil, err
		}
	}
	if deleteBranch && wt.Branch != "" {
		// -D (not -d): the caller has decided to discard the branch, which is usually
		// unmerged (otherwise retention would have taken the merged path); `-d` would
		// refuse and leave the branch behind AFTER its worktree is gone — half-done and
		// confusing.
		if _, err := gitOut(ctx, main, "branch", "-D", wt.Branch); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

// finishCheckoutRemoval turns a failed `git worktree remove` into one of the two honest
// outcomes instead of a bare 500:
//
//   - the checkout is GONE as far as git is concerned (git drops the files first and the
//     registration last, so on Windows a process still holding the directory leaves an
//     empty one behind after the entry is already unregistered): carry on, delete the
//     branch as asked, and return a warning naming the directory that survived. Never
//     leave "directory present, branch still there";
//   - the checkout is still registered: nothing is deleted, and ErrWorktreeBusy says so
//     with the reason, so the caller knows the removal did not happen at all.
//
// A plain `git worktree remove` refusal for uncommitted changes is reported as
// ErrWorktreeDirty: a probe can race the job that writes into the tree, and the caller's
// answer to that is --force, not a retry.
func finishCheckoutRemoval(ctx context.Context, main string, wt *worktreeRef, force bool, cause error) ([]string, error) {
	if strings.Contains(cause.Error(), "contains modified or untracked files") {
		return nil, fmt.Errorf("%w: %s", ErrWorktreeDirty, wt.Path)
	}
	if force {
		// git could not finish the directory (a locked file); with --force the contents are
		// already meant to be discarded, so delete whatever is left of it ourselves.
		if err := os.RemoveAll(wt.Path); err != nil {
			slog.Warn("job.worktree_removal_incomplete", "event", "job.worktree_removal_incomplete",
				"component", "job", "path", wt.Path, "err", err)
		}
	} else if isDir(wt.Path) {
		// Without --force the tree may hold work the caller did not agree to discard;
		// leave the files alone and let the registration decide the outcome below.
		if worktreeRegistered(ctx, main, wt.Path) {
			return nil, fmt.Errorf("%w: %s: %v", ErrWorktreeBusy, wt.Path, cause)
		}
	}
	if worktreeRegistered(ctx, main, wt.Path) {
		// A registered entry whose directory is gone is exactly what prune drops; run it so
		// the repository stops advertising the checkout.
		if _, err := gitOut(ctx, main, "worktree", "prune"); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrWorktreeBusy, wt.Path, err)
		}
		if worktreeRegistered(ctx, main, wt.Path) {
			return nil, fmt.Errorf("%w: %s: %v", ErrWorktreeBusy, wt.Path, cause)
		}
	}
	if isDir(wt.Path) {
		return []string{fmt.Sprintf("worktree directory %s could not be deleted (a process may still be using it); "+
			"the branch and the git worktree entry are cleaned up — delete the directory later", wt.Path)}, nil
	}
	return nil, nil
}

// worktreeRegistered reports whether path is still a registered worktree of the
// repository at main. The comparison goes through util.RealPath because git reports the
// resolved path while the job row may carry a differently spelled one.
func worktreeRegistered(ctx context.Context, main, path string) bool {
	out, err := gitOut(ctx, main, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	want := util.RealPath(path)
	for _, line := range strings.Split(out, "\n") {
		entry, ok := strings.CutPrefix(strings.TrimRight(line, "\r"), "worktree ")
		if !ok {
			continue
		}
		if util.RealPath(strings.TrimSpace(entry)) == want {
			return true
		}
	}
	return false
}

// worktreeUsers names the jobs that still work in the managed worktree at path: a row
// whose process has not ended (status !IsFinished, which covers queued/running/waiting
// for a directory slot and a job paused for approval). A continuation job records its
// source's worktree, so this is also how an auto-resume continuation keeps its checkout
// from being deleted under it.
func (s *Service) worktreeUsers(path string) []string {
	if s.meta == nil {
		return nil
	}
	recs, err := s.meta.ListWorktrees()
	if err != nil {
		slog.Warn("job.worktree_users", "event", "job.worktree_users", "component", "job", "path", path, "err", err)
		return nil
	}
	want := util.RealPath(path)
	var users []string
	for _, r := range recs {
		if IsFinished(r.Status) || util.RealPath(r.Path) != want {
			continue
		}
		users = append(users, r.JobID+" ("+r.Status+")")
	}
	return users
}

// removeWorktreeMerged removes a worktree whose branch has just been merged: the same
// unlink-then-remove dance as removeWorktree, but the branch goes away with `git branch
// -d`, which refuses a branch that is not fully merged into its upstream.
func removeWorktreeMerged(ctx context.Context, wt *worktreeRef) error {
	main, err := mainCheckout(ctx, wt.Path)
	if err != nil {
		return err
	}
	removeWorktreeLinks(wt.Path, wt.Links)
	sweepWorktreeLinks(wt.Path, wt.Links)
	if _, err := gitOut(ctx, main, "worktree", "remove", "--force", wt.Path); err != nil {
		return err
	}
	if wt.Branch != "" {
		if _, err := gitOut(ctx, main, "branch", "-d", wt.Branch); err != nil {
			return err
		}
	}
	return nil
}

// mainCheckout resolves the repository's MAIN worktree from a managed worktree
// path: `<common git dir>/..`. For a normal checkout `--git-common-dir` is the
// relative ".git", so the result is the checkout itself.
func mainCheckout(ctx context.Context, wtPath string) (string, error) {
	common, err := gitOut(ctx, wtPath, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(wtPath, common)
	}
	return filepath.Dir(filepath.Clean(common)), nil
}

// worktreeMerged reports whether the worktree branch is already contained in the
// base branch, i.e. deleting it loses nothing. "Base branch" is the branch checked
// out in the repository's MAIN worktree: that is the lineage the worktree was
// forked from (base defaults to that checkout's HEAD), and it is the branch a
// reviewer merges the deliverable into. A branch with NOTHING ahead of its base is
// trivially merged. Any git failure reads as "not merged" — the safe side, since it
// only means the worktree is KEPT.
func worktreeMerged(ctx context.Context, wt *worktreeRef, commitsAhead int) bool {
	if commitsAhead == 0 {
		return true
	}
	if wt.Branch == "" {
		return false
	}
	main, err := mainCheckout(ctx, wt.Path)
	if err != nil {
		return false
	}
	if _, err := gitOut(ctx, main, "merge-base", "--is-ancestor", wt.Branch, "HEAD"); err == nil {
		return true
	}
	return worktreeSquashMerged(ctx, main, wt.Branch)
}

// worktreeSquashMerged recognises a branch whose changes already live in HEAD without
// its commits being ancestors (a --squash merge or a cherry-pick): merging it again
// would change nothing, i.e. `git merge-tree --write-tree HEAD <branch>` yields exactly
// HEAD's tree. Any failure (old git without --write-tree, a would-be conflict) reads as
// "not merged" — the safe side.
func worktreeSquashMerged(ctx context.Context, main, branch string) bool {
	merged, err := gitOut(ctx, main, "merge-tree", "--write-tree", "HEAD", branch)
	if err != nil {
		return false
	}
	head, err := gitOut(ctx, main, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return false
	}
	return strings.TrimSpace(merged) == strings.TrimSpace(head)
}

// cleanupTempWorktree removes a `ref:` job's temporary checkout (design 一·3). finish()
// calls it for every terminal path, so a job that failed, was cancelled or timed out
// leaves no checkout behind either — and so does the shutdown path (a checkout must never
// outlive the job that owned it). The removal is the managed-worktree one — unlink the
// recorded dependency links, sweep any link the list missed, then `git worktree remove
// --force` — with no branch to delete (a temp checkout has none), retried a few times
// because a process may still hold the directory for a moment on Windows.
//
// Best-effort by design: it never changes the job's outcome. A removal that still fails is
// recorded as an event (and logged), and the next sweep — server/worker startup, or the
// periodic one in serve — finds the checkout by its project's worktree dir template under
// the repository it was created in and removes it (see SweepStaleTempCheckouts).
func (s *Service) cleanupTempWorktree(entry *jobEntry) {
	s.cleanupTempWorktreeWithin(entry, worktreeTimeout)
}

// cleanupTempWorktreeWithin is cleanupTempWorktree with a caller-chosen budget: the
// shutdown path uses a shorter one so a directory a process still holds cannot stall the
// process exit (the next startup's sweep finishes the job).
func (s *Service) cleanupTempWorktreeWithin(entry *jobEntry, budget time.Duration) {
	entry.mu.Lock()
	wt := entry.wt
	if wt == nil || !wt.Temp {
		entry.mu.Unlock()
		return
	}
	// The row must stop advertising a checkout that is about to be gone; the resolved
	// commit stays in worktree_base_sha for audit.
	entry.wt = nil
	entry.result.WorktreePath = ""
	jobID := entry.result.ID
	entry.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	if err := removeTempCheckout(ctx, wt); err != nil {
		slog.Warn("job.worktree_removed_failed", "event", "job.worktree_removed_failed", "component", "job",
			"job_id", jobID, "path", wt.Path, "err", err)
		s.recordEvent(jobID, EventJobTempCheckoutLeft, map[string]any{
			"path": wt.Path, "ref": wt.BaseSHA, "error": err.Error(),
		})
		return
	}
	slog.Info("job.worktree_removed", "event", "job.worktree_removed", "component", "job",
		"job_id", jobID, "path", wt.Path, "ref", wt.BaseSHA, "reason", "temporary ref checkout")
}

// removeTempCheckout removes one temporary `ref:` checkout, retrying a locked directory
// (Windows releases file handles asynchronously after the job's process tree ended) and
// falling back to deleting what git left behind. Both the finish path and the stale sweep
// go through here so a checkout is only ever removed the safe way round: the dependency
// links first (the link itself, never its target), then the checkout, then the git
// registration.
func removeTempCheckout(ctx context.Context, wt *worktreeRef) error {
	// Links first and once: unlinking is idempotent and must never be part of a retry that
	// could race a still-live junction walk.
	removeWorktreeLinks(wt.Path, wt.Links)
	sweepWorktreeLinks(wt.Path, wt.Links)
	var lastErr error
	for attempt := 1; attempt <= tempCheckoutAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("%w: %s (attempt %d/%d, last error: %v)", ErrWorktreeBusy, wt.Path, attempt, tempCheckoutAttempts, lastErr)
			case <-time.After(tempCheckoutRetryDelay * time.Duration(attempt-1)):
			}
		}
		warnings, err := removeWorktreeWithWarnings(ctx, wt, true, false)
		if err == nil && len(warnings) == 0 && !isDir(wt.Path) {
			return nil
		}
		if isDir(wt.Path) {
			// git could not finish the directory (a half-created checkout git never registered,
			// or a file a process still holds). The links are already unlinked above, so this
			// delete can never walk into the main checkout through one.
			sweepWorktreeLinks(wt.Path, wt.Links)
			if rerr := os.RemoveAll(wt.Path); rerr == nil && !isDir(wt.Path) {
				if wt.Top != "" {
					_, _ = gitOut(ctx, wt.Top, "worktree", "prune")
				}
				return nil
			} else if rerr != nil {
				err = rerr
			} else {
				err = fmt.Errorf("%w: %s", ErrWorktreeBusy, wt.Path)
			}
		}
		if err == nil {
			err = fmt.Errorf("%w: %s", ErrWorktreeBusy, wt.Path)
		}
		lastErr = err
	}
	return lastErr
}

// isDir reports whether p exists and is a directory.
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
