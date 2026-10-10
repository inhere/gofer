package commands

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/gcli/v3"

	"github.com/gookit/goutil/errorx"
	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// workerInitFlags holds `gofer worker init` (and the `gofer init worker --server …`
// delegation that reuses the same flow) flags.
type workerInitFlags struct {
	server     string
	token      string
	adminToken string
	id         string
	roots      gcli.Strings
	projects   gcli.Strings
	yes        bool
	force      bool
	workspace  string
	timeout    string
}

var workerInitOpts = workerInitFlags{}

// workerTokenEnv is the env var the generated worker.yaml reads its hub token from
// and the .env keys it under.
const workerTokenEnv = "GOFER_WORKER_TOKEN"

// workerInitDetector is the detect seam the wizard probes the host with (swapped in
// tests so a generated config never depends on the developer's PATH).
var workerInitDetector agent.Detector = agent.DefaultDetector()

// workerInitStdin is where the interactive prompts read from (swapped in tests).
var workerInitStdin io.Reader = os.Stdin

// NewWorkerInitCmd builds `gofer worker init <flags>`: the CFG-05 one-command worker
// onboarding. It asks the server which projects this worker may run, infers the roots
// mapping onto this machine's paths, probes the installed agent CLIs, writes
// <config-dir>/worker.yaml + <config-dir>/.env, then runs the doctor against the file
// it just wrote and prints the startup command. It NEVER starts the worker (how a
// daemon is started differs by platform) and never touches a running one.
func NewWorkerInitCmd(info buildinfo.Info) *gcli.Command {
	return &gcli.Command{
		Name: "init",
		Desc: "Onboard THIS machine as a worker in one command: ask the server for its projects, infer roots, probe agents, write worker.yaml, run doctor",
		Config: func(c *gcli.Command) {
			c.StrOpt(&workerInitOpts.server, "server", "s", "", "hub address, e.g. http://<server-ip>:8767 (required)")
			c.StrOpt(&workerInitOpts.token, "token", "", "", "this worker's hub token (written to <config-dir>/.env as GOFER_WORKER_TOKEN; defaults to an already-exported GOFER_WORKER_TOKEN)")
			c.StrOpt(&workerInitOpts.adminToken, "admin-token", "", "", "server administrator token: register this worker once (never written to disk)")
			c.StrOpt(&workerInitOpts.id, "id", "", "", "worker_id (must equal the server's server.workers key; required)")
			c.VarOpt(&workerInitOpts.roots, "roots", "", "explicit roots mapping from=to (repeatable; wins over inference)")
			c.VarOpt(&workerInitOpts.projects, "project", "", "project to allow during registration (repeatable)")
			c.BoolOpt(&workerInitOpts.yes, "yes", "y", false, "non-interactive: accept every inferred value without prompting")
			c.BoolOpt(&workerInitOpts.force, "force", "f", false, "overwrite an existing worker.yaml (the old file is backed up to worker.yaml.bak-<time>)")
			c.StrOpt(&workerInitOpts.workspace, "workspace", "", "", "directory to create as the default workspace (default: $GOFER_WORKSPACE, else ~/.gofer/workspace)")
			c.StrOpt(&workerInitOpts.timeout, "timeout", "", "10s", "per-check timeout for the doctor run, e.g. 10s")
		},
		Func: func(c *gcli.Command, _ []string) error { return runWorkerInit(c, info) },
	}
}

// runInitDelegatedWorker runs the wizard from the top-level `gofer init worker
// --server …` entry point, mapping that command's flags onto the wizard's. It keeps
// ONE implementation for both spellings (design §二).
func runInitDelegatedWorker(c *gcli.Command, info buildinfo.Info) error {
	prev := workerInitOpts
	defer func() { workerInitOpts = prev }()
	workerInitOpts = workerInitFlags{
		server:     initOpts.server,
		token:      initOpts.token,
		adminToken: initOpts.adminToken,
		id:         initOpts.id,
		roots:      initOpts.roots,
		projects:   initOpts.projects,
		yes:        initOpts.yes,
		force:      initOpts.force,
		workspace:  initOpts.workspace,
		timeout:    initOpts.timeout,
	}
	return runWorkerInit(c, info)
}

// runWorkerInit performs the onboarding flow. Every network call is read-only
// (assignable list + the doctor's register probe); the only writes are the generated
// worker.yaml / .env, always under the resolved config dir.
func runWorkerInit(c *gcli.Command, info buildinfo.Info) error {
	id := strings.TrimSpace(workerInitOpts.id)
	if id == "" {
		return errorx.Failf(configExitErr, "--id is required (worker_id must equal the server's server.workers key)")
	}
	if err := config.CheckWorkerID(id); err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	if strings.TrimSpace(workerInitOpts.server) == "" {
		return errorx.Failf(configExitErr, "--server is required (the hub address, e.g. http://<server-ip>:8767)")
	}
	token := strings.TrimSpace(workerInitOpts.token)
	if token == "" {
		token = os.Getenv(workerTokenEnv)
	}
	timeout, err := workerInitTimeout()
	if err != nil {
		return err
	}

	cfgDir, err := config.ConfigDir()
	if err != nil {
		return errorx.Failf(configExitErr, "resolve config dir: %v", err)
	}
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return errorx.Failf(configExitErr, "create config dir %s: %v", cfgDir, err)
	}
	workerPath := filepath.Join(cfgDir, config.WorkerConfigFileName)
	envPath := filepath.Join(cfgDir, config.EnvFileName)
	// Refuse to clobber a real worker.yaml BEFORE any network work: the operator gets
	// the actionable error even when the hub is unreachable (design §二, D6 mirror).
	if _, statErr := os.Stat(workerPath); statErr == nil && !workerInitOpts.force {
		return errorx.Failf(configExitErr, "%s already exists; use --force to overwrite (the old file is backed up)", workerPath)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return errorx.Failf(configExitErr, "stat %s: %v", workerPath, statErr)
	}

	if token == "" && strings.TrimSpace(workerInitOpts.adminToken) == "" {
		return errorx.Failf(configExitErr, "--token or --admin-token is required (or export %s)", workerTokenEnv)
	}
	if token == "" {
		adminClient, err := newClient(config.InputCfgFile, workerInitOpts.server, strings.TrimSpace(workerInitOpts.adminToken))
		if err != nil {
			return err
		}
		reg, err := adminClient.RegisterWorker(id, nil, workerInitOpts.projects)
		if err != nil {
			return errorx.Failf(configExitErr, "register worker %s: %v", id, err)
		}
		token = reg.WorkerToken
		c.Printf("✓ 已向 server 登记 worker %s，管理员 token 未写入磁盘\n", id)
	}
	cli, err := newClient(config.InputCfgFile, workerInitOpts.server, token)
	if err != nil {
		return err
	}
	assignable, err := cli.WorkerAssignable(id)
	if err != nil {
		return errorx.Failf(configExitErr, "ask server %s about worker %q: %v", workerInitOpts.server, id, err)
	}
	serverVersion := assignable.ServerVersion
	if strings.TrimSpace(serverVersion) == "" {
		serverVersion = "(dev build: no version stamped)"
	}
	c.Printf("✓ 连接 server %s，协议 v%d，可派给 %s 的项目 %d 个\n",
		serverVersion, assignable.ProtocolVersion, id, len(assignable.Projects))
	warnIfWorkerOnline(c, cli, id)

	hostPaths := make([]string, 0, len(assignable.Projects))
	for _, p := range assignable.Projects {
		c.Printf("  %-24s host_path %s\n", p.Key, p.HostPath)
		if strings.TrimSpace(p.HostPath) != "" {
			hostPaths = append(hostPaths, p.HostPath)
		}
	}

	roots, err := resolveWorkerInitRoots(c, hostPaths)
	if err != nil {
		return err
	}
	if len(roots) == 0 {
		c.Printf("warning: 没有可用的 roots 映射 —— 这台 worker 目前跑不了任何 server 项目；补上 --roots <from>=<to> 后重跑\n")
	}

	agentKeys, detected := detectedAgents(workerInitDetector)
	c.Printf("探测到 agents（已装 %d 个）：\n", len(agentKeys))
	for _, key := range sortedDetectKeys(detected) {
		res := detected[key]
		mark, detail := "✗", "未安装"
		if res.Available {
			mark, detail = "✓", res.Version
		}
		c.Printf("  %s %-12s %s\n", mark, key, detail)
	}
	if len(agentKeys) == 0 {
		c.Printf("提示: 本机没有装任何可用的 agent CLI，生成的 worker 只能跑 exec job；装好后重跑本命令或手改 agents 段\n")
	}

	wsDir, err := ensureWorkspaceDir(workerInitOpts.workspace)
	if err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}

	body, err := renderWorkerConfig(id, workerInitOpts.server, roots, agentConfigsFor(workerInitDetector, agentKeys))
	if err != nil {
		return errorx.Failf(configExitErr, "render worker.yaml: %v", err)
	}
	if err := backupAndWrite(workerPath, body, workerInitOpts.force); err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	if err := upsertEnvFile(envPath, workerTokenEnv, token); err != nil {
		return errorx.Failf(configExitErr, "%v", err)
	}
	// The doctor below resolves the token through the environment, so export the very
	// token this run used — a fresh machine has not exported it yet, and a dotenv load
	// would NOT be enough (config.LoadDotenv deliberately skips every *_TOKEN key while
	// the process is running inside a job, see its leak-1 guard). Setting it here means
	// the doctor verifies the registration with exactly the credential the worker will
	// start with.
	if err := os.Setenv(workerTokenEnv, token); err != nil {
		c.Printf("warning: 设置 %s 失败：%v\n", workerTokenEnv, err)
	}
	c.Printf("已写入 %s、%s\n", workerPath, envPath)
	c.Printf("%s\n", inlineWorkspaceHint(wsDir))

	rep := buildWorkerDoctorReport(workerPath, timeout, info, workerInitDetector, true)
	out, rerr := renderWorkerDoctor(rep, false)
	if rerr != nil {
		return rerr
	}
	c.Printf("运行 doctor：\n%s", out)
	if rep.Failures > 0 {
		return errorx.Failf(workerDoctorExitErr, "worker init: doctor 有 %d 项失败，修好上面的项后重跑 `gofer worker doctor`", rep.Failures)
	}
	c.Printf("启动：gofer worker -d        (worker 常驻后台；Windows 登录自启任务仅服务端有脚本，worker 请用该命令或做服务)\n")
	return nil
}

// workerInitTimeout parses the wizard's --timeout (same syntax/default as the doctor).
func workerInitTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(workerInitOpts.timeout)
	if raw == "" {
		return 10 * time.Second, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, errorx.Failf(configExitErr, "invalid --timeout %q: %v", raw, err)
	}
	if d <= 0 {
		return 0, errorx.Failf(configExitErr, "invalid --timeout %q: must be positive", raw)
	}
	return d, nil
}

// warnIfWorkerOnline warns when the id being onboarded is ALREADY connected on the
// hub: registering it will replace that live connection (and fail its in-flight
// jobs), so the operator has to know before starting the second instance.
// Best-effort: an unreachable/older server just skips the check.
func warnIfWorkerOnline(c *gcli.Command, cli *client.Client, id string) {
	workers, err := cli.ListWorkers()
	if err != nil {
		return
	}
	for _, w := range workers {
		if w.ID == id && w.Connected {
			c.Printf("warning: server 上 %s 已有一个在线的 worker —— 启动本机会顶掉它的连接并失败其 in-flight job；确认是要替换再启动\n", id)
			return
		}
	}
}

// resolveWorkerInitRoots resolves the roots mapping: explicit --roots wins; otherwise
// the inference from the server-side project paths, confirmed interactively unless
// --yes. Missing local trees are reported (a root that maps nothing still lets the
// wizard finish, but the operator must know which projects will fail).
func resolveWorkerInitRoots(c *gcli.Command, hostPaths []string) ([]config.WorkerRoot, error) {
	if len(workerInitOpts.roots) > 0 {
		roots, err := parseWorkerRootFlags(workerInitOpts.roots)
		if err != nil {
			return nil, errorx.Failf(configExitErr, "%v", err)
		}
		c.Printf("使用显式 --roots 映射 %d 条（跳过推断）\n", len(roots))
		return roots, nil
	}
	inferred, notFound := inferWorkerRoots(hostPaths, osStatExists)
	if len(inferred) == 0 {
		return nil, nil
	}
	for _, hp := range notFound {
		c.Printf("warning: 项目目录在本机不存在（映射后）：%s —— 该 project 会派发失败，用 --roots 精确指定\n", hp)
	}
	if workerInitOpts.yes {
		return inferred, nil
	}
	return confirmRoots(c, inferred)
}

// confirmRoots walks the inferred roots through the operator: Enter (or y) accepts,
// anything else is read as a replacement local path. A root the operator drops is
// simply not written (roots only WIDEN what the worker may run).
func confirmRoots(c *gcli.Command, inferred []config.WorkerRoot) ([]config.WorkerRoot, error) {
	sc := bufio.NewScanner(workerInitStdin)
	out := make([]config.WorkerRoot, 0, len(inferred))
	for _, r := range inferred {
		state := "✓ 存在"
		if !osStatExists(r.To) {
			state = "✗ 不存在"
		}
		c.Printf("  %s → %s  [%s]  接受? [Y/n/改] ", r.From, r.To, state)
		if !sc.Scan() {
			return nil, errorx.Failf(configExitErr, "stdin closed while confirming roots")
		}
		switch answer := strings.TrimSpace(sc.Text()); strings.ToLower(answer) {
		case "", "y", "yes":
			out = append(out, r)
		case "n", "no":
			c.Printf("    已跳过 %s（该前缀下的项目不会被派到本机）\n", r.From)
		default:
			out = append(out, config.WorkerRoot{From: r.From, To: answer})
		}
	}
	return out, nil
}

// parseWorkerRootFlags parses repeated `from=to` into roots.
func parseWorkerRootFlags(raw []string) ([]config.WorkerRoot, error) {
	out := make([]config.WorkerRoot, 0, len(raw))
	for _, item := range raw {
		from, to, ok := strings.Cut(item, "=")
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if !ok || from == "" || to == "" {
			return nil, fmt.Errorf("invalid --roots %q (want <server-side prefix>=<local path>)", item)
		}
		out = append(out, config.WorkerRoot{From: from, To: to})
	}
	return out, nil
}

// inferWorkerRoots maps the server-side project paths onto this machine. The LONGEST
// COMMON PREFIX of those paths becomes the root's `from`; its local form becomes `to`
// (the same path when it exists here, else the drive-letter translation D:/x ↔ /d/x).
// Every project is then checked against the local filesystem and the ones that do not
// resolve are returned separately — an inferred root that silently maps to nothing is
// exactly the failure this wizard exists to prevent.
//
// exists is the filesystem probe (osStatExists in production) so the translation rule
// is testable on a host that only has one of the two shapes.
func inferWorkerRoots(hostPaths []string, exists func(string) bool) (roots []config.WorkerRoot, notFound []string) {
	paths := make([]string, 0, len(hostPaths))
	for _, p := range hostPaths {
		if strings.TrimSpace(p) != "" {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return nil, nil
	}
	prefix := commonPathPrefix(paths)
	to := localRootFor(prefix, exists)
	if to == "" {
		return nil, append([]string(nil), paths...)
	}
	roots = append(roots, config.WorkerRoot{From: prefix, To: to})
	for _, hp := range paths {
		if !exists(mapLogicalToLocal(hp, prefix, to)) {
			notFound = append(notFound, hp)
		}
	}
	sort.Strings(notFound)
	return roots, notFound
}

// localRootFor returns the local form of a server-side prefix: the same path when it
// exists here, else the drive-letter translation, else "" (nothing inferable).
func localRootFor(prefix string, exists func(string) bool) string {
	if exists(prefix) {
		return prefix
	}
	if alt := translateDrivePath(prefix); alt != prefix && exists(alt) {
		return alt
	}
	return ""
}

// translateDrivePath converts between the two spellings of the same Windows/container
// path: `D:/work/x` ⇄ `/d/work/x` (a drive mounted as a single-letter dir, the shape
// a Linux container sees). A path with no drive letter is returned unchanged.
func translateDrivePath(p string) string {
	s := strings.ReplaceAll(p, `\`, "/")
	if len(s) >= 3 && s[1] == ':' && s[2] == '/' && isASCIILetter(s[0]) {
		return "/" + strings.ToLower(s[:1]) + s[2:]
	}
	if len(s) >= 3 && s[0] == '/' && isASCIILetter(s[1]) && s[2] == '/' {
		return strings.ToUpper(s[1:2]) + ":" + s[2:]
	}
	return s
}

// mapLogicalToLocal rewrites a server-side path onto the local root: the prefix is
// replaced by `to` and the remainder appended verbatim.
func mapLogicalToLocal(p, prefix, to string) string {
	n := strings.ReplaceAll(p, `\`, "/")
	np := strings.ReplaceAll(prefix, `\`, "/")
	if n == np {
		return to
	}
	if strings.HasPrefix(n, strings.TrimSuffix(np, "/")+"/") {
		return strings.TrimSuffix(to, "/") + n[len(np):]
	}
	return to
}

// commonPathPrefix returns the longest segment-aligned common prefix of the paths
// (normalised to '/'). A single path is its own prefix; unrelated paths give "".
func commonPathPrefix(paths []string) string {
	segs := func(p string) []string {
		return strings.Split(strings.Trim(strings.ReplaceAll(p, `\`, "/"), "/"), "/")
	}
	first := segs(paths[0])
	// A leading drive letter must match exactly; deeper segments too (the mapping is
	// a prefix rule, so a case difference would silently mis-map).
	n := len(first)
	for _, p := range paths[1:] {
		s := segs(p)
		if len(s) < n {
			n = len(s)
		}
		for i := 0; i < n; i++ {
			if s[i] != first[i] {
				n = i
				break
			}
		}
	}
	if n == 0 {
		return ""
	}
	joined := strings.Join(first[:n], "/")
	// Preserve a leading slash for POSIX paths (Split drops it).
	if strings.HasPrefix(strings.ReplaceAll(paths[0], `\`, "/"), "/") {
		return "/" + joined
	}
	return joined
}

// isASCIILetter reports whether b is an ASCII letter (drive letters only).
func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// osStatExists is the production existence probe: a readable directory.
func osStatExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// renderWorkerConfig renders the generated worker.yaml: a short header (why the file
// looks machine-written and how to change it) plus the marshalled config.
func renderWorkerConfig(id, server string, roots []config.WorkerRoot, agents map[string]config.AgentConfig) ([]byte, error) {
	hubURL, err := workerHubWSURL(server)
	if err != nil {
		return nil, err
	}
	wc := config.WorkerConfig{
		WorkerID:   id,
		ServerLink: config.WorkerServerLink{URLs: []string{hubURL}, TokenEnv: workerTokenEnv},
		Roots:      roots,
		Agents:     agents,
	}
	body, err := yaml.Marshal(wc)
	if err != nil {
		return nil, err
	}
	header := "# gofer worker config — generated by `gofer worker init` (CFG-05).\n" +
		"# 改完用 `gofer worker doctor` 自检；roots 只在【本机】维护(服务端看不到)。\n" +
		"# token 从 .env 的 " + workerTokenEnv + " 读, 不写进本文件。\n"
	return append([]byte(header), body...), nil
}

// workerHubWSURL turns the wizard's --server into the hub websocket address a worker
// dials: http(s) → ws(s), a bare host:port → ws://, and the fixed /v1/workers/connect
// path when none was given.
func workerHubWSURL(server string) (string, error) {
	raw := strings.TrimSpace(server)
	if raw == "" {
		return "", fmt.Errorf("empty server address")
	}
	if !strings.Contains(raw, "://") {
		raw = "ws://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse server %q: %w", server, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("server %q: scheme must be http(s) or ws(s)", server)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server %q: missing host", server)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/v1/workers/connect"
	}
	return u.String(), nil
}

// backupAndWrite backs the existing file up to <path>.bak-<time> (when --force
// replaced it) and then atomically replaces <path> with body: rename-over means a
// crash mid-write can never leave a half-written config behind for a restarting
// worker to read.
func backupAndWrite(path string, body []byte, force bool) error {
	if force {
		if old, err := os.ReadFile(path); err == nil {
			bak := fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
			if werr := os.WriteFile(bak, old, 0o600); werr != nil {
				return fmt.Errorf("back up %s: %w", path, werr)
			}
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// upsertEnvFile sets KEY=value in the dotenv at path, preserving every other line (the
// file may hold the node's other credentials). It is created when absent; a rewritten
// value is replaced in place, so re-running the wizard is idempotent.
func upsertEnvFile(path, key, value string) error {
	var lines []string
	if b, err := os.ReadFile(path); err == nil {
		lines = strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	prefix := key + "="
	replaced := false
	for i, ln := range lines {
		if strings.HasPrefix(strings.TrimSpace(ln), prefix) {
			lines[i] = prefix + value
			replaced = true
		}
	}
	if !replaced {
		lines = append(lines, prefix+value)
	}
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
