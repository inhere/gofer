package commands

import (
	"fmt"
	"os"
	"sort"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/config"
)

// initAgentDetector is the detect pass the init commands use to decide which agent
// CLIs this host actually has. It is a package var (like the other CLI seams) so
// tests can make the generated config deterministic instead of depending on the
// developer's PATH; production uses the same probe as every other assembly path.
var initAgentDetector agent.Detector = agent.DefaultDetector()

// defaultWorkspaceProjectKey is the project key `gofer init` registers for the
// default workspace (F-g).
const defaultWorkspaceProjectKey = "default"

// detectedAgentKeys probes the host through det and returns the agent keys that are
// actually installed, sorted. Only TEMPLATE-INJECTED keys count, so an operator's
// escape-hatch declarations (none exist at init time — the config is empty) can never
// leak in; `exec` is excluded because it is built in and needs no declaration.
func detectedAgentKeys(det agent.Detector) []string {
	resolved, _ := agent.Resolve(&config.Config{}, det)
	keys := make([]string, 0, len(resolved.InjectedAgents()))
	for key := range resolved.InjectedAgents() {
		if key == agent.ExecAgentKey {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// sortedDetectKeys returns the probed agent keys in a stable order, so the wizard's
// report does not shuffle between runs (Go map iteration is random). `exec` is left
// out: it is built in and is never something the operator has to install.
func sortedDetectKeys(detected map[string]agent.DetectResult) []string {
	keys := make([]string, 0, len(detected))
	for key := range detected {
		if key == agent.ExecAgentKey {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// detectedAgents returns the agent keys this host reports, with their detect result,
// sorted — the wizard's "探测到 agents: claude ✓ omp ✓ codex ✗" line and the source of
// the agents block it writes (only the available ones are declared).
func detectedAgents(det agent.Detector) ([]string, map[string]agent.DetectResult) {
	resolved, detected := agent.Resolve(&config.Config{}, det)
	keys := make([]string, 0, len(resolved.InjectedAgents()))
	for key := range resolved.InjectedAgents() {
		if key == agent.ExecAgentKey {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, detected
}

// agentConfigsFor returns the resolved config entries of the DETECTED agent keys, so
// a generated worker.yaml declares exactly the CLIs this host has. Undetected
// templates are absent by construction (agent.Resolve only injects available ones),
// and the built-in exec agent is left out — it needs no declaration.
func agentConfigsFor(det agent.Detector, keys []string) map[string]config.AgentConfig {
	resolved, _ := agent.Resolve(&config.Config{}, det)
	out := make(map[string]config.AgentConfig, len(keys))
	for _, key := range keys {
		if ac, ok := resolved.Agents[key]; ok {
			out[key] = ac
		}
	}
	return out
}

// defaultWorkspaceProject is the project config `gofer init server` registers for the
// default workspace: the directory itself, the local runner, and whatever agents this
// host actually has (an EMPTY allowed_agents means "any configured agent", so a host
// with no CLI installed still gets a usable project).
func defaultWorkspaceProject(hostPath string, agents []string) config.ProjectConfig {
	p := config.ProjectConfig{
		HostPath:       hostPath,
		AllowedRunners: []string{config.BuiltinLocalRunner},
	}
	if len(agents) > 0 {
		p.AllowedAgents = agents
	}
	return p
}

// insertDefaultProjectEntry inserts a `default:` entry as the FIRST child of the
// template's top-level `projects:` mapping. A textual insert (not a yaml round trip)
// is deliberate: the shipped template's value is its comments, which re-marshalling
// would throw away.
func insertDefaultProjectEntry(tmpl string, proj config.ProjectConfig) (string, error) {
	raw, err := yaml.Marshal(map[string]config.ProjectConfig{defaultWorkspaceProjectKey: proj})
	if err != nil {
		return "", err
	}
	block := indentBlock(strings.TrimRight(string(raw), "\n"), "  ")

	lines := strings.Split(tmpl, "\n")
	for i, ln := range lines {
		if strings.TrimRight(ln, " \t\r") != "projects:" {
			continue
		}
		out := make([]string, 0, len(lines)+len(strings.Split(block, "\n"))+4)
		out = append(out, lines[:i+1]...)
		out = append(out,
			"  # F-g: 默认工作空间 —— 临时/不属于任何仓库的活的安全落脚处(可用 --workspace /",
			"  # GOFER_WORKSPACE 改路径)。`job run` 不带 -p 且当前目录匹配不到任何项目时回落到它。",
		)
		out = append(out, strings.Split(block, "\n")...)
		out = append(out, lines[i+1:]...)
		return strings.Join(out, "\n"), nil
	}
	return "", fmt.Errorf("embedded template has no top-level `projects:` key to extend")
}

// indentBlock prefixes every line of s with prefix (an empty line stays empty).
func indentBlock(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if ln == "" {
			continue
		}
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// existingDefaultHostPath returns the host_path of the `default` project already
// declared in the config at path, or "" when the file is absent/unreadable/without
// one. It is how `gofer init --force` keeps an operator's own default project instead
// of replacing it with the computed workspace (F-g: 已有的 default 不覆盖).
func existingDefaultHostPath(path string) string {
	if path == "" {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	cfg, _, err := config.Load(path)
	if err != nil || cfg == nil {
		return ""
	}
	p, ok := cfg.Projects[defaultWorkspaceProjectKey]
	if !ok {
		return ""
	}
	return p.HostPath
}

// applyDefaultWorkspace rewrites a fresh server config so it carries the default
// workspace project (F-g): it creates the workspace directory (unless an existing
// `default` project already points somewhere — that path is kept as-is) and inserts
// the project into the template's `projects:` mapping.
func applyDefaultWorkspace(path, tmpl, explicitWorkspace string) (string, error) {
	wsDir, err := config.WorkspaceDir(explicitWorkspace)
	if err != nil {
		return "", fmt.Errorf("resolve default workspace: %w", err)
	}
	if keep := existingDefaultHostPath(path); keep != "" {
		// The config already has a default project: keep ITS directory (do not create
		// or move anything) and just carry the entry forward.
		return insertDefaultProjectEntry(tmpl, defaultWorkspaceProject(keep, detectedAgentKeys(initAgentDetector)))
	}
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		return "", fmt.Errorf("create default workspace %s: %w", wsDir, err)
	}
	return insertDefaultProjectEntry(tmpl, defaultWorkspaceProject(wsDir, detectedAgentKeys(initAgentDetector)))
}

// inlineWorkspaceHint is the one-liner the worker wizard prints about the default
// workspace: a POLICY worker does not own its project list (the server pushes it), so
// the directory is created locally and the project is registered server-side.
func inlineWorkspaceHint(wsDir string) string {
	return fmt.Sprintf("默认工作空间 %s 已就绪：把它登记为 server 上的 `%s` 项目（allowed_runners 含本 worker 的 runner）后即可承接无仓库的活",
		wsDir, defaultWorkspaceProjectKey)
}

// ensureWorkspaceDir creates the (flag/env/home-resolved) workspace directory and
// returns its path. Shared by `gofer init server` and the worker wizard; an existing
// directory is left untouched.
func ensureWorkspaceDir(explicit string) (string, error) {
	dir, err := config.WorkspaceDir(explicit)
	if err != nil {
		return "", fmt.Errorf("resolve default workspace: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create default workspace %s: %w", dir, err)
	}
	return dir, nil
}
