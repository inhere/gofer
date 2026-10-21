package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	yaml "github.com/goccy/go-yaml"
)

// initGitTimeout bounds the one git probe `wf init` makes (reading the remote HEAD).
const initGitTimeout = 5 * time.Second

// Workflow init (`gofer wf init verify|release`) writes a starter file into the
// repository, pre-filled from what the repository actually contains: a Go module
// becomes `go test` + `go vet` fans, a package.json contributes its test / build /
// lint scripts, and `.github/workflows` names pre-fill the wait-ci `required` list
// (design 2026-10-21 §二). The file is a normal project template: the server reads it
// from the project workspace's `.gofer/workflows`, which is why the generated text
// ends with the "commit it before the server can read it" reminder.
const (
	// InitVerify is the kind of the check workflow starter.
	InitVerify = "verify"
	// InitRelease is the kind of the release workflow starter.
	InitRelease = "release"
)

// ErrWorkflowFileExists is returned by WriteWorkflowInit when the target file is
// already there and the caller did not ask to overwrite it.
var ErrWorkflowFileExists = errors.New("workflow file already exists")

// WorkflowInitPath returns where the starter of kind lives in the repository at dir.
func WorkflowInitPath(kind, dir string) string {
	return filepath.Join(dir, ".gofer", "workflows", kind+".yaml")
}

// GenerateWorkflowInit renders the starter file of kind (verify or release) for the
// repository at dir. The generated text is validated the way a template is
// (ValidateWorkflowTemplateContent), so `wf init` can never write a file that a run
// would refuse.
func GenerateWorkflowInit(kind, dir string) (string, error) {
	facts := inspectRepo(dir)
	var content string
	switch kind {
	case InitVerify:
		content = renderVerifyInit(facts)
	case InitRelease:
		content = renderReleaseInit(facts)
	default:
		return "", fmt.Errorf("unknown workflow kind %q (want %s or %s)", kind, InitVerify, InitRelease)
	}
	if _, err := ValidateWorkflowTemplateContent(content); err != nil {
		return "", fmt.Errorf("generated %s workflow is invalid: %w", kind, err)
	}
	return content, nil
}

// WriteWorkflowInit generates the starter of kind and writes it to the repository at
// dir. An existing file is left alone (ErrWorkflowFileExists) unless force is set, so
// a second `wf init` never silently discards the edits an operator made.
func WriteWorkflowInit(kind, dir string, force bool) (string, error) {
	path := WorkflowInitPath(kind, dir)
	if _, err := os.Stat(path); err == nil && !force {
		return path, fmt.Errorf("%w: %s (pass --force to overwrite it)", ErrWorkflowFileExists, path)
	}
	content, err := GenerateWorkflowInit(kind, dir)
	if err != nil {
		return path, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return path, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return path, fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// nodeProject is one package.json found in the repository: its directory relative to
// the repository root ("" for the root) and the scripts worth fanning out over.
type nodeProject struct {
	dir     string
	scripts []string
	// manager is the package manager implied by the lock file (npm when unknown).
	manager string
}

// repoFacts is what `wf init` reads from the repository to pre-fill a starter file.
type repoFacts struct {
	dir string
	// goMod is whether the repository root has a go.mod.
	goMod bool
	// node lists the package.json files found (root first, then sub-directories).
	node []nodeProject
	// workflows are the `name:` values of .github/workflows/*.yml, in file order.
	workflows []string
	// branch is the repository's default branch ("" when it could not be read).
	branch string
}

// nodeScriptOrder is the order the check scripts are fanned out in: the ones a
// verify step wants first, in the order `npm run` conventions suggest.
var nodeScriptOrder = []string{"test", "build", "lint"}

// inspectRepo reads the facts a starter file is pre-filled from. Every probe is
// best-effort: a missing file simply contributes nothing.
func inspectRepo(dir string) repoFacts {
	facts := repoFacts{dir: dir}
	facts.goMod = fileExists(filepath.Join(dir, "go.mod"))
	facts.node = findNodeProjects(dir)
	facts.workflows = readGitHubWorkflows(filepath.Join(dir, ".github", "workflows"))
	facts.branch = defaultBranch(dir)
	return facts
}

// findNodeProjects returns the root package.json and every package.json one directory
// below the root (a `web/`, `frontend/` or `ui/` is common), skipping the directories
// that never carry the project's own scripts.
func findNodeProjects(dir string) []nodeProject {
	var out []nodeProject
	if p, ok := readNodeProject(dir, ""); ok {
		out = append(out, p)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	for _, ent := range entries {
		if !ent.IsDir() || skipNodeDir(ent.Name()) {
			continue
		}
		if p, ok := readNodeProject(filepath.Join(dir, ent.Name()), ent.Name()); ok {
			out = append(out, p)
		}
	}
	return out
}

// skipNodeDir names the sub-directories that never hold the repository's own
// package.json scripts.
func skipNodeDir(name string) bool {
	switch name {
	case "node_modules", "dist", "build", "vendor", ".git", ".gofer", ".worktrees":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// readNodeProject reads one package.json and keeps the scripts a verify step can fan
// out over, in nodeScriptOrder.
func readNodeProject(dir, rel string) (nodeProject, bool) {
	body, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nodeProject{}, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(body, &pkg); err != nil {
		return nodeProject{}, false
	}
	var scripts []string
	for _, name := range nodeScriptOrder {
		if _, ok := pkg.Scripts[name]; ok {
			scripts = append(scripts, name)
		}
	}
	if len(scripts) == 0 {
		return nodeProject{}, false
	}
	return nodeProject{dir: rel, scripts: scripts, manager: detectPackageManager(dir)}, true
}

// detectPackageManager picks the command that runs a script from the lock file next
// to package.json: pnpm-lock.yaml -> pnpm, yarn.lock -> yarn, everything else npm.
func detectPackageManager(dir string) string {
	switch {
	case fileExists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm"
	case fileExists(filepath.Join(dir, "yarn.lock")):
		return "yarn"
	default:
		return "npm"
	}
}

// readGitHubWorkflows collects the `name:` of every workflow file, which is what the
// wait-ci `required` list matches against. A file without a name: is skipped (its
// file name is not what the checks API reports).
func readGitHubWorkflows(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(ent.Name()))
		if ext != ".yml" && ext != ".yaml" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, ent.Name()))
		if err != nil {
			continue
		}
		var doc struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(body, &doc); err != nil {
			continue
		}
		if name := strings.TrimSpace(doc.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// defaultBranch reads the branch the repository's remote HEAD points at ("main" when
// it cannot be determined: no git, no remote, or a bare test directory).
func defaultBranch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), initGitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output()
	if err != nil {
		return "main"
	}
	ref := strings.TrimSpace(string(out))
	ref = strings.TrimPrefix(ref, "origin/")
	if ref == "" || ref == "HEAD" {
		return "main"
	}
	return ref
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// renderVerifyInit renders the verify starter: one `checks` step whose fans are the
// repository's real checks, so a first run already does something useful (design §二:
// one step, several fans, `join: all`).
func renderVerifyInit(f repoFacts) string {
	var b strings.Builder
	b.WriteString("# gofer workflow: verify\n")
	b.WriteString("# 由 `gofer wf init verify` 按仓库内容生成，可按需修改。\n")
	b.WriteString("# 运行：gofer wf run --template verify -p <项目> --var commit=<提交>\n")
	b.WriteString("#\n")
	b.WriteString("# 改法说明：\n")
	b.WriteString("# - ref: 提交时解析成 sha，在临时检出上执行（无分支，结束即删），每一路检查的是同一份代码。\n")
	b.WriteString("# - join: all: 各路并行跑完，任一路失败则整个步骤失败；结果见 `gofer wf show` 的结果表。\n")
	b.WriteString("# - agent: exec: 直接执行 cmd；换成 claude / codex 则改为由该 agent 执行 prompt。\n")
	b.WriteString("# - 每一步之间串行；要并行就放进同一步骤的 fan 里。\n")
	b.WriteString("title: \"verify ${vars.commit}\"\n")
	b.WriteString("vars:\n")
	b.WriteString("  commit: {required: true, desc: \"要检查的提交（sha / 分支 / tag）\"}\n")
	b.WriteString("steps:\n")
	b.WriteString("  - name: checks\n")
	b.WriteString("    agent: exec\n")
	b.WriteString("    ref: ${vars.commit}\n")
	fans := verifyFans(f)
	if len(fans) == 0 {
		// Nothing to pre-fill: one placeholder command on the step itself (a single fan
		// can not carry join: all, and a fan list of one would only hide the shape).
		b.WriteString("    # 没有识别到 go.mod / package.json：把下面这一行换成真正的检查命令。\n")
		b.WriteString("    cmd: [\"echo\", \"把这一行换成真正的检查命令\"]\n")
	} else {
		b.WriteString("    join: all\n")
		b.WriteString("    fan:\n")
		b.WriteString("      # 每一路是一个独立检查：name 用于结果表，也是 ${steps.checks.<name>.exit_code} 的引用名。\n")
		for _, fan := range fans {
			b.WriteString("      - " + fan + "\n")
		}
	}
	b.WriteString("      # 常用改法（取消注释后按需调整）：\n")
	b.WriteString("      # - name: go-test-race\n")
	b.WriteString("      #   cmd: [go, test, -race, -count=1, ./...]\n")
	b.WriteString("      # - name: vet-darwin\n")
	b.WriteString("      #   cmd: [go, vet, ./...]\n")
	b.WriteString("      #   env: {GOOS: darwin}          # 环境变量随请求下发给 worker，不要放秘密\n")
	b.WriteString("      # - name: test-windows\n")
	b.WriteString("      #   runner: <worker-id>          # 指定在另一台执行机（worker）上跑\n")
	b.WriteString("      #   cmd: [go, test, -count=1, ./...]\n")
	b.WriteString("      # - name: web\n")
	b.WriteString("      #   cwd: web                     # 在子目录执行\n")
	b.WriteString("      #   cmd: [sh, -c, \"npx vue-tsc --noEmit && npx vitest run\"]\n")
	b.WriteString("#\n")
	b.WriteString("# 提交到仓库后 server 才能读到这份文件（server 从项目工作区的 .gofer/workflows 读取）。\n")
	return b.String()
}

// verifyFans lists the fan entries the repository's contents justify: Go checks first,
// then one entry per node script. Each entry is `name:` on its first line and the rest
// indented by 8 spaces, which is where it lands under `      - ` in the step's fan list.
func verifyFans(f repoFacts) []string {
	var fans []string
	if f.goMod {
		fans = append(fans,
			"name: go-test\n        cmd: [go, test, ./...]",
			"name: go-vet\n        cmd: [go, vet, ./...]")
	}
	for _, proj := range f.node {
		for _, script := range proj.scripts {
			fans = append(fans, nodeFanLine(proj, script))
		}
	}
	return fans
}

// nodeFanLine renders one `<manager> run <script>` fan entry, pinning cwd when the
// package.json lives in a sub-directory and giving the fan a name that stays unique
// across the projects.
func nodeFanLine(proj nodeProject, script string) string {
	name := script
	if proj.dir != "" {
		// A fan name is an identifier: `web/test` would also collide with the `/`-separated
		// reference syntax, so the directory becomes part of the name instead.
		name = strings.NewReplacer("/", "-", " ", "-").Replace(proj.dir) + "-" + script
	}
	line := fmt.Sprintf("name: %s\n        cmd: [%s, run, %s]", name, proj.manager, script)
	if proj.dir != "" {
		line += fmt.Sprintf("\n        cwd: %s", proj.dir)
	}
	return line
}

// renderReleaseInit renders the release starter: push the branch and the version tag,
// then wait for the CI checks of that tag (design §二).
func renderReleaseInit(f repoFacts) string {
	branch := f.branch
	if branch == "" {
		branch = "main"
	}
	var b strings.Builder
	b.WriteString("# gofer workflow: release\n")
	b.WriteString("# 由 `gofer wf init release` 按仓库内容生成，可按需修改。\n")
	b.WriteString("# 运行：gofer wf run --template release -p <项目> --var version=X.Y.Z\n")
	b.WriteString("#\n")
	b.WriteString("# 步骤按顺序执行（步骤之间串行）：\n")
	b.WriteString("# 1. push: 推送分支与 tag，推完用 ls-remote 核对远端 sha。\n")
	b.WriteString("#    需要项目的 delivery.allow_push，并且调用方持有 repo.push 权限。\n")
	b.WriteString("#    失败按 on_failure: retry 重试（每次尝试是独立 job，不累积单 job 时限）。\n")
	b.WriteString("# 2. ci: 等 GitHub Actions 的检查全部结束；required 里的检查失败即失败。\n")
	b.WriteString("# 3. assets（默认注释掉）: 等 Release 上出现产物。\n")
	b.WriteString("title: \"release v${vars.version}\"\n")
	b.WriteString("vars:\n")
	b.WriteString("  version: {required: true, desc: \"版本号，例如 1.2.3（tag 加倍为 v${vars.version}）\"}\n")
	b.WriteString("steps:\n")
	b.WriteString("  - name: push\n")
	b.WriteString("    uses: gofer/push\n")
	b.WriteString("    with:\n")
	b.WriteString("      refs: [" + yamlScalar(branch) + ", \"v${vars.version}\"]\n")
	b.WriteString("      # remote: origin          # 推送的远端，默认 origin\n")
	b.WriteString("    on_failure: retry\n")
	b.WriteString("    retry: {max_attempts: 5, backoff_sec: [60, 300]}\n")
	b.WriteString("  - name: ci\n")
	b.WriteString("    uses: gofer/wait-ci\n")
	b.WriteString("    with:\n")
	b.WriteString("      ref: \"v${vars.version}\"\n")
	if len(f.workflows) > 0 {
		b.WriteString("      # required 取 .github/workflows 里各 workflow 的 name:，按实际需要增删。\n")
		b.WriteString("      required: [" + yamlScalarList(f.workflows) + "]\n")
	} else {
		b.WriteString("      # 没有读到 .github/workflows 的 name:；需要时填上必需的检查名。\n")
		b.WriteString("      # required: [<检查名>]\n")
	}
	b.WriteString("      # code_scanning: true     # 打开后只看该 ref 新增的代码扫描告警\n")
	b.WriteString("      timeout_min: 40\n")
	b.WriteString("  # 3. 等 Release 产物（把 assets 换成实际产物名，可用通配符）\n")
	b.WriteString("  # - name: assets\n")
	b.WriteString("  #   uses: gofer/wait-assets\n")
	b.WriteString("  #   with: {tag: \"v${vars.version}\", assets: [\"*linux-amd64*\"], timeout_min: 30}\n")
	b.WriteString("#\n")
	b.WriteString("# 提交到仓库后 server 才能读到这份文件（server 从项目工作区的 .gofer/workflows 读取）。\n")
	return b.String()
}

// yamlScalar quotes a value only when its plain form would not survive YAML.
func yamlScalar(s string) string {
	if routingKeyRe.MatchString(s) {
		return s
	}
	return fmt.Sprintf("%q", s)
}

// yamlScalarList renders a YAML flow sequence of scalars.
func yamlScalarList(items []string) string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, yamlScalar(item))
	}
	return strings.Join(out, ", ")
}

