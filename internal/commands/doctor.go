package commands

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	yaml "github.com/goccy/go-yaml"
	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/agent"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/servicemgr"
	"github.com/inhere/gofer/internal/util"
)

var doctorOpts struct {
	output       string
	print        bool
	lines        int
	workerConfig string
	bundle       bool
}

type diagnosticFile struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes,omitempty"`
	Error string `json:"error,omitempty"`
	data  []byte
}

type diagnosticManifest struct {
	Format    int              `json:"format"`
	CreatedAt time.Time        `json:"created_at"`
	Gofer     string           `json:"gofer"`
	Mode      string           `json:"mode"`
	Files     []diagnosticFile `json:"files"`
	Summary   string           `json:"summary"`
}

type diagnosticMetadata struct {
	GoferVersion string   `json:"gofer_version"`
	GitCommit    string   `json:"git_commit,omitempty"`
	BuildDate    string   `json:"build_date,omitempty"`
	OS           string   `json:"os"`
	Arch         string   `json:"arch"`
	Go           string   `json:"go"`
	Mode         string   `json:"mode"`
	Config       string   `json:"config,omitempty"`
	WorkerConfig string   `json:"worker_config,omitempty"`
	DotenvFiles  []string `json:"dotenv_files,omitempty"`
}

// NewDoctorCmd builds the privacy-safe diagnostic exporter. It deliberately does
// not collect job prompts, messages, memories, or arbitrary process environment values.
func NewDoctorCmd(info buildinfo.Info) *gcli.Command {
	return &gcli.Command{
		Name:     "doctor",
		Desc:     cliText("cli.doctor.desc"),
		Help:     cliText("cli.doctor.help"),
		Examples: cliText("cli.doctor.examples"),
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			c.StrOpt(&doctorOpts.output, "output", "o", "", cliText("cli.doctor.output_flag"))
			c.BoolOpt(&doctorOpts.print, "print", "", false, cliText("cli.doctor.print_flag"))
			c.BoolOpt(&doctorOpts.bundle, "bundle", "", true, cliText("cli.doctor.bundle_flag"))
			c.IntOpt(&doctorOpts.lines, "lines", "n", 500, cliText("cli.doctor.lines_flag"))
			c.StrOpt(&doctorOpts.workerConfig, "worker-config", "", "", cliText("cli.doctor.worker_config_flag"))
		},
		Func: func(c *gcli.Command, _ []string) error { return runDoctor(c, info) },
	}
}

func runDoctor(c *gcli.Command, info buildinfo.Info) error {
	if !doctorOpts.bundle && !doctorOpts.print {
		return errors.New(cliText("cli.doctor.print_required"))
	}
	if doctorOpts.lines < 1 || doctorOpts.lines > 10000 {
		return errors.New(cliText("cli.doctor.lines_invalid"))
	}
	files, manifest := collectDiagnostics(info, doctorOpts.lines)
	manifest.Files = make([]diagnosticFile, 0, len(files))
	for _, f := range files {
		manifest.Files = append(manifest.Files, diagnosticFile{Name: f.Name, Bytes: len(f.data), Error: f.Error})
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if doctorOpts.print {
		c.Printf("%s\n", cliText("cli.doctor.summary"))
		for _, f := range manifest.Files {
			if f.Error != "" {
				c.Printf("- %-24s %s\n", f.Name, cliText("cli.doctor.unavailable"))
			} else {
				c.Printf("- %-24s %d bytes\n", f.Name, f.Bytes)
			}
		}
		return nil
	}
	path := doctorOpts.output
	if path == "" {
		path = "gofer-diagnostics-" + time.Now().Format("20060102-150405") + ".zip"
	}
	if err := writeDiagnosticZip(path, files, manifestBytes); err != nil {
		return err
	}
	c.Printf("%s\n", cliText("cli.doctor.created", "path", path))
	c.Printf("%s\n", cliText("cli.doctor.contents"))
	for _, f := range manifest.Files {
		if f.Error == "" {
			c.Printf("- %s\n", f.Name)
		} else {
			c.Printf("- %s (%s)\n", f.Name, cliText("cli.doctor.unavailable"))
		}
	}
	return nil
}

func collectDiagnostics(info buildinfo.Info, lines int) ([]diagnosticFile, diagnosticManifest) {
	mode := config.RunMode()
	manifest := diagnosticManifest{Format: 1, CreatedAt: time.Now().UTC(), Gofer: info.DisplayVersion(), Mode: mode, Summary: "privacy-safe diagnostic data; prompts, messages, memories and token values are excluded"}
	files := make([]diagnosticFile, 0, 10)
	addJSON := func(name string, value any) {
		b, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			files = append(files, diagnosticFile{Name: name, Error: "unavailable"})
			return
		}
		files = append(files, diagnosticFile{Name: name, data: append(b, '\n')})
	}
	addText := func(name string, b []byte, err error) {
		if err != nil {
			files = append(files, diagnosticFile{Name: name, Error: "unavailable"})
			return
		}
		files = append(files, diagnosticFile{Name: name, data: b})
	}

	cfg, cfgPath, cfgErr := config.Load(config.InputCfgFile)
	workerPath := ""
	var wc config.WorkerConfig
	var workerErr error
	if mode == config.RunModeWorker {
		workerPath = workerConfigPath()
		loaded, loadErr := loadWorkerConfig(workerPath)
		workerErr = loadErr
		if loadErr == nil && loaded != nil {
			wc = *loaded
		}
	}
	meta := diagnosticMetadata{GoferVersion: info.DisplayVersion(), GitCommit: info.GitCommit, BuildDate: info.BuildDate, OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version(), Mode: mode, Config: cfgPath, WorkerConfig: workerPath, DotenvFiles: config.LoadedDotenvFiles()}
	addJSON("metadata.json", meta)
	if cfgErr != nil {
		addJSON("config.json", map[string]string{"error": "configuration could not be loaded"})
	} else {
		redacted, err := redactedConfigJSON(cfg)
		addText("config.json", redacted, err)
	}
	if mode == config.RunModeWorker {
		if workerErr != nil {
			addJSON("worker-config.json", map[string]string{"error": "worker configuration could not be loaded"})
		} else {
			redacted, err := redactedYAMLJSON(wc)
			addText("worker-config.json", redacted, err)
		}
	}

	secrets := diagnosticSecrets(cfg, wc, cfgErr == nil, workerErr == nil)
	if mode != config.RunModeWorker {
		if localWorkerPath, err := config.UserWorkerConfigPath(); err == nil {
			if localWorker, err := loadWorkerConfig(localWorkerPath); err == nil {
				workerView, viewErr := redactedYAMLJSON(localWorker)
				addText("worker-config.json", workerView, viewErr)
				secrets = append(secrets, sensitiveYAMLValues(localWorker)...)
				workerLog, tailErr := diagnosticLogTail(workerLogFile(localWorker.WorkerID), lines, secrets)
				addText("logs/worker.log", workerLog, tailErr)
			}
		}
	}
	if mode == config.RunModeWorker && workerErr == nil {
		path := workerLogFile(wc.WorkerID)
		b, err := diagnosticLogTail(path, lines, secrets)
		addText("logs/worker.log", b, err)
		b, err = diagnosticLogTail(workerOutFile(wc.WorkerID), lines, secrets)
		addText("logs/worker-out.log", b, err)
	}
	if mode != config.RunModeWorker || workerErr != nil {
		path := config.RuntimeFilePath("run", "serve.log")
		if cfgErr == nil {
			path = configuredLogPath(cfg, cfgPath, path)
		}
		b, err := diagnosticLogTail(path, lines, secrets)
		addText("logs/server.log", b, err)
	}

	if m, err := managerFor(servicemgr.DefaultName); err != nil {
		addJSON("serve-status.json", map[string]string{"error": "service manager unavailable"})
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		status, err := inspectManagedStatus(ctx, m)
		cancel()
		if err != nil {
			addJSON("serve-status.json", map[string]string{"error": "managed service status unavailable"})
		} else {
			addJSON("serve-status.json", map[string]any{
				"name": status.Name, "backend": status.Backend, "registered": status.Registered,
				"native_state": status.NativeState, "active": status.Active,
				"server_verified": status.ServerVerified, "port_owned": status.PortOwned,
				"health": status.Health, "registered_version": status.RegisteredVersion,
				"running_version": status.RunningVersion, "config_files": status.ConfigFiles,
				"dotenv_files": status.DotenvFiles, "auth_token": status.AuthToken,
			})
		}
	}

	if cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token); err != nil {
		addJSON("workers.json", map[string]string{"error": "server connection unavailable"})
		addJSON("agents.json", map[string]string{"error": "server connection unavailable"})
	} else {
		workers, werr := cli.ListWorkers()
		agents, aerr := cli.ListAgents()
		if werr != nil {
			addJSON("workers.json", map[string]string{"error": "worker list unavailable"})
		} else {
			brief := make([]map[string]any, 0, len(workers))
			for _, w := range workers {
				brief = append(brief, map[string]any{
					"id": w.ID, "connected": w.Connected, "labels": w.Labels,
					"projects": w.Projects, "agents": w.Agents,
				})
			}
			addJSON("workers.json", brief)
		}
		if aerr != nil {
			addJSON("agents.json", map[string]string{"error": "agent list unavailable"})
		} else {
			brief := make([]map[string]any, 0, len(agents))
			for _, a := range agents {
				brief = append(brief, map[string]any{"name": a.Name, "type": a.Type, "available": a.Available})
			}
			addJSON("agents.json", brief)
		}
	}
	if cfgErr == nil {
		addJSON("local-agents.json", detectLocalAgents(cfg))
	}
	manifest.Files = nil
	return files, manifest
}

func workerConfigPath() string {
	if strings.TrimSpace(doctorOpts.workerConfig) != "" {
		return doctorOpts.workerConfig
	}
	path, err := config.UserWorkerConfigPath()
	if err != nil {
		return ""
	}
	return path
}

func configuredLogPath(cfg *config.Config, cfgPath, fallback string) string {
	if cfg == nil {
		return fallback
	}
	if cfg.Log.File != "" {
		if p, err := config.ResolveLocalPath("log.file", cfg.Log.File); err == nil {
			if filepath.IsAbs(p) {
				return p
			}
			return filepath.Join(filepath.Dir(cfgPath), p)
		}
	}
	if cfg.Log.Dir != "" {
		if p, err := config.ResolveLocalPath("log.dir", cfg.Log.Dir); err == nil {
			if !filepath.IsAbs(p) {
				p = filepath.Join(filepath.Dir(cfgPath), p)
			}
			return filepath.Join(p, "serve.log")
		}
	}
	return fallback
}

func diagnosticLogTail(path string, lines int, secrets []string) ([]byte, error) {
	last, _, err := serveLogTail(path, lines)
	if err != nil {
		return nil, err
	}
	return []byte(redactDiagnosticText(string(last), secrets)), nil
}

func detectLocalAgents(cfg *config.Config) []map[string]any {
	resolved, detected := agent.Resolve(cfg, agent.DefaultDetector())
	keys := make([]string, 0, util.CapSum(len(resolved.Agents), 1))
	for key := range resolved.Agents {
		keys = append(keys, key)
	}
	if _, ok := resolved.Agents[agent.ExecAgentKey]; !ok {
		keys = append(keys, agent.ExecAgentKey)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		res := detected[key]
		out = append(out, map[string]any{"key": key, "available": res.Available, "version": res.Version, "error": res.Error})
	}
	return out
}

func redactedConfigJSON(value any) ([]byte, error) { return redactedYAMLJSON(value) }

func redactedYAMLJSON(value any) ([]byte, error) {
	y, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	var raw any
	if err := yaml.Unmarshal(y, &raw); err != nil {
		return nil, err
	}
	redactDiagnosticValue(&raw, "")
	return json.MarshalIndent(raw, "", "  ")
}

func redactDiagnosticValue(value *any, key string) {
	switch v := (*value).(type) {
	case map[string]any:
		for k, child := range v {
			redactDiagnosticValue(&child, k)
			v[k] = child
		}
	case map[any]any:
		for k, child := range v {
			ks := fmt.Sprint(k)
			redactDiagnosticValue(&child, ks)
			v[k] = child
		}
	case []any:
		for i := range v {
			redactDiagnosticValue(&v[i], key)
		}
	case string:
		key = strings.ToLower(key)
		switch {
		case strings.Contains(key, "token") && !strings.HasSuffix(key, "_env"):
			*value = config.TokenMask(v)
		case strings.Contains(key, "url") || strings.Contains(v, "://"):
			*value = redactDiagnosticURL(v)
		case key == "type" || key == "addr" || key == "path_view" || key == "language":
			// The same non-secret settings exposed by the configuration read view.
		case v != "":
			// Fail closed: env/header maps, custom commands, prompts, webhook URLs
			// without a standard key and future config strings never leave the host.
			*value = "(configured)"
		}
	}
}

func diagnosticSensitiveKey(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), " ", "_"))
	return strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password") || strings.Contains(key, "credential") || strings.Contains(key, "private_key")
}

func redactDiagnosticURL(raw string) string {
	redacted := config.RedactConfigURL(raw)
	u, err := url.Parse(redacted)
	if err != nil || u.Host == "" {
		return redacted
	}
	// URL paths may embed credentials (e.g. webhook bot access keys).
	u.Path, u.RawPath, u.Fragment, u.RawFragment = "", "", "", ""
	return u.String()
}

func diagnosticSecrets(cfg *config.Config, wc config.WorkerConfig, haveCfg, haveWorker bool) []string {
	var out []string
	if haveCfg {
		out = append(out, sensitiveYAMLValues(cfg)...)
		out = append(out, sensitiveYAMLValues(cfg.Server)...)
	}
	if haveWorker {
		out = append(out, sensitiveYAMLValues(wc)...)
	}
	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if ok && value != "" && (strings.Contains(strings.ToUpper(name), "TOKEN") || strings.Contains(strings.ToUpper(name), "SECRET") || strings.Contains(strings.ToUpper(name), "PASSWORD")) {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

func sensitiveYAMLValues(value any) []string {
	b, err := yaml.Marshal(value)
	if err != nil {
		return nil
	}
	var raw any
	if yaml.Unmarshal(b, &raw) != nil {
		return nil
	}
	var out []string
	var walk func(any, string)
	walk = func(v any, key string) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				walk(child, k)
			}
		case map[any]any:
			for k, child := range x {
				walk(child, fmt.Sprint(k))
			}
		case []any:
			for _, child := range x {
				walk(child, key)
			}
		case string:
			if diagnosticSensitiveKey(key) && x != "" && !strings.HasSuffix(strings.ToLower(key), "_env") && !strings.HasSuffix(strings.ToLower(key), "_file") {
				out = append(out, x)
			}
		}
	}
	walk(raw, "")
	return out
}

var (
	diagnosticCredential = regexp.MustCompile(`(?i)(?:bearer\s+|(?:token|secret|password|passwd|api[_-]?key|authorization|credential)\s*[=:]\s*["']?)[^\s"',;{}]+`)
	diagnosticURL        = regexp.MustCompile(`(?i)(?:https?|wss?)://[^\s"'<>]+`)
	diagnosticPayload    = regexp.MustCompile(`(?i)prompt|message|memory|mail|letter|conversation|request_json|system_prompt|\bcontent\b|\bbody\b`)
	diagnosticOpaque     = regexp.MustCompile(`\b[A-Za-z0-9_-]{32,}\b`)
)

func redactDiagnosticText(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, config.TokenMask(secret))
		}
	}
	parts := strings.Split(text, "\n")
	for i, line := range parts {
		if diagnosticPayload.MatchString(line) {
			parts[i] = "[payload omitted]"
			continue
		}
		line = diagnosticURL.ReplaceAllStringFunc(line, redactDiagnosticURL)
		line = diagnosticCredential.ReplaceAllStringFunc(line, func(credential string) string {
			return "[credential redacted]"
		})
		parts[i] = diagnosticOpaque.ReplaceAllString(line, "[opaque value redacted]")
	}
	return strings.Join(parts, "\n")
}

func writeDiagnosticZip(path string, files []diagnosticFile, manifest []byte) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	write := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := write("manifest.json", manifest); err != nil {
		zw.Close()
		return err
	}
	for _, item := range files {
		if item.Error == "" {
			if err := write(item.Name, item.data); err != nil {
				zw.Close()
				return err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}
