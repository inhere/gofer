package job

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/runner"
	"github.com/inhere/gofer/internal/util"
)

// mcpConfigFileName is the claude-json MCP config gofer writes into a job's result dir.
const mcpConfigFileName = "mcp.json"

// mcpInjection is how one job's bound MCP servers reach its agent (MCP-06 §3.4).
// Secret values only ever travel in Env (the agent process's environment) or, for an
// acp-agent, inside the in-memory session/new parameters (ACP); argv, the config file
// and every persisted record carry variable references.
type mcpInjection struct {
	// Args is a ready argv fragment spliced before the prompt argument (codex-config).
	Args []string
	// UseMCPArgs asks the caller to splice the agent's mcp_args (claude-json), whose
	// {{mcp_config_file}} names ConfigFile.
	UseMCPArgs bool
	ConfigFile string
	// Env is added to the agent process's environment.
	Env map[string]string
	// ACP is the session/new mcpServers list (acp format).
	ACP []runner.ACPMCPServer
	// Notes are limitations worth telling the user (written to the job's stderr).
	Notes []string
}

// buildMCPInjection prepares the injection of names (declared in specs) for an agent
// using format. lookup reads the EXECUTING machine's environment; a secret variable
// it does not have fails the job here, naming the variable. resultDir receives the
// claude-json config file.
func buildMCPInjection(format string, names []string, specs map[string]config.MCPServerConfig, resultDir string, lookup func(string) (string, bool)) (mcpInjection, error) {
	inj := mcpInjection{Env: map[string]string{}}
	secret := func(server, kind, key, src string) (string, error) {
		v, ok := lookup(src)
		if !ok {
			return "", fmt.Errorf("MCP server %q: environment variable %s (%s %s) is not set on this machine", server, src, kind, key)
		}
		return v, nil
	}
	claudeServers := map[string]any{}
	// codex passes secret_env through under the variable's own name, so two servers
	// may not want the same name from different sources.
	codexPass := map[string]string{}
	for _, name := range names {
		spec, ok := specs[name]
		if !ok {
			return inj, fmt.Errorf("MCP server %q is not declared", name)
		}
		secretEnv := map[string]string{}
		for _, k := range sortedMapKeys(spec.SecretEnv) {
			v, err := secret(name, "secret_env", k, spec.SecretEnv[k])
			if err != nil {
				return inj, err
			}
			secretEnv[k] = v
		}
		secretHeaders := map[string]string{}
		for _, k := range sortedMapKeys(spec.SecretHeaders) {
			v, err := secret(name, "secret_headers", k, spec.SecretHeaders[k])
			if err != nil {
				return inj, err
			}
			secretHeaders[k] = v
		}
		if len(spec.Tools) > 0 && format != config.MCPFormatCodexConfig {
			inj.Notes = append(inj.Notes, fmt.Sprintf("MCP server %q: tools filter is not supported by %s agents; all of its tools are available", name, format))
		}
		switch format {
		case config.MCPFormatACP:
			srv := runner.ACPMCPServer{Name: name, Type: spec.EffectiveType()}
			plain := func(s string) string {
				return config.ReplaceMCPEnvRefs(s, func(k string) string { return secretEnv[k] })
			}
			if srv.Type == config.MCPTypeHTTP {
				srv.URL = plain(spec.URL)
				srv.Headers = mergeStrings(spec.Headers, secretHeaders)
			} else {
				srv.Command = spec.Command
				for _, a := range spec.Args {
					srv.Args = append(srv.Args, plain(a))
				}
				srv.Env = mergeStrings(spec.Env, secretEnv)
			}
			inj.ACP = append(inj.ACP, srv)

		case config.MCPFormatClaudeJSON:
			ref := func(k string) string {
				v := config.MCPInjectedEnvName(name, k)
				return "${" + v + "}"
			}
			for k, v := range secretEnv {
				inj.Env[config.MCPInjectedEnvName(name, k)] = v
			}
			for k, v := range secretHeaders {
				inj.Env[config.MCPInjectedEnvName(name, k)] = v
			}
			if spec.EffectiveType() == config.MCPTypeHTTP {
				headers := map[string]string{}
				for k, v := range spec.Headers {
					headers[k] = v
				}
				for k := range secretHeaders {
					headers[k] = ref(k)
				}
				entry := map[string]any{"type": "http", "url": config.ReplaceMCPEnvRefs(spec.URL, ref)}
				if len(headers) > 0 {
					entry["headers"] = headers
				}
				claudeServers[name] = entry
			} else {
				args := make([]string, 0, len(spec.Args))
				for _, a := range spec.Args {
					args = append(args, config.ReplaceMCPEnvRefs(a, ref))
				}
				env := map[string]string{}
				for k, v := range spec.Env {
					env[k] = v
				}
				for k := range secretEnv {
					env[k] = ref(k)
				}
				entry := map[string]any{"command": spec.Command, "args": args}
				if len(env) > 0 {
					entry["env"] = env
				}
				claudeServers[name] = entry
			}

		case config.MCPFormatCodexConfig:
			if strings.Contains(name, ".") {
				return inj, fmt.Errorf("MCP server %q: a name with '.' cannot be passed to codex (-c splits keys on '.'); rename it", name)
			}
			refs := config.MCPEnvRefs(spec.URL)
			for _, a := range spec.Args {
				refs = append(refs, config.MCPEnvRefs(a)...)
			}
			if len(refs) > 0 {
				return inj, fmt.Errorf("MCP server %q: codex cannot expand {{env:%s}} in args / url; pass the secret through secret_env instead", name, refs[0])
			}
			key := "mcp_servers." + name + "."
			set := func(k, tomlValue string) { inj.Args = append(inj.Args, "-c", key+k+"="+tomlValue) }
			if spec.EffectiveType() == config.MCPTypeHTTP {
				set("url", tomlString(spec.URL))
				if len(spec.Headers) > 0 {
					set("http_headers", tomlInlineTable(spec.Headers))
				}
				if len(secretHeaders) > 0 {
					refsByHeader := map[string]string{}
					for k, v := range secretHeaders {
						envName := config.MCPInjectedEnvName(name, k)
						inj.Env[envName] = v
						refsByHeader[k] = envName
					}
					set("env_http_headers", tomlInlineTable(refsByHeader))
				}
			} else {
				set("command", tomlString(spec.Command))
				set("args", tomlArray(spec.Args))
				if len(spec.Env) > 0 {
					set("env", tomlInlineTable(spec.Env))
				}
				if len(secretEnv) > 0 {
					var pass []string
					for _, k := range sortedMapKeys(secretEnv) {
						if prev, dup := codexPass[k]; dup && prev != spec.SecretEnv[k] {
							return inj, fmt.Errorf("MCP servers bound to this codex job read %s from different variables (%s, %s); codex passes it under one name", k, prev, spec.SecretEnv[k])
						}
						codexPass[k] = spec.SecretEnv[k]
						inj.Env[k] = secretEnv[k]
						pass = append(pass, k)
					}
					set("env_vars", tomlArray(pass))
				}
			}
			if len(spec.Tools) > 0 {
				set("enabled_tools", tomlArray(spec.Tools))
			}

		default:
			return inj, fmt.Errorf("MCP servers cannot be injected into this agent (mcp_format %q)", format)
		}
	}
	if format == config.MCPFormatClaudeJSON && len(claudeServers) > 0 {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(map[string]any{"mcpServers": claudeServers}); err != nil {
			return inj, err
		}
		path := filepath.Join(resultDir, mcpConfigFileName)
		if err := os.MkdirAll(resultDir, 0o755); err != nil {
			return inj, err
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
			return inj, fmt.Errorf("write %s: %w", path, err)
		}
		inj.ConfigFile = path
		inj.UseMCPArgs = true
	}
	return inj, nil
}

func sortedMapKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mergeStrings(a, b map[string]string) map[string]string {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make(map[string]string, util.CapSum(len(a), len(b)))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// tomlString renders a TOML basic string (JSON string escapes are valid TOML).
func tomlString(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(buf.String(), "\n")
}

func tomlArray(items []string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, tomlString(it))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// tomlInlineTable renders {key = "value", …}; keys are quoted so any header name works.
func tomlInlineTable(m map[string]string) string {
	parts := make([]string, 0, len(m))
	for _, k := range sortedMapKeys(m) {
		parts = append(parts, tomlString(k)+"="+tomlString(m[k]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}
