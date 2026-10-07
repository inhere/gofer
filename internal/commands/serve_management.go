package commands

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gookit/gcli/v3"
	"github.com/inhere/gofer/internal/buildinfo"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/daemon"
	"github.com/inhere/gofer/internal/servicemgr"
)

type serveRegisterOptions struct {
	name, exe, workDir, scope, runAs               string
	addr, webDir                                   string
	elevated, adopt, start, noWeb, allowEmptyToken bool
}

type serveManageOptions struct {
	name   string
	asJSON bool
}
type serveLogsOptions struct {
	name            string
	lines           int
	follow, journal bool
}
type serveUpgradeOptions struct {
	name, binary string
	noWait       bool
}
type serveUpgradeStatusOptions struct {
	name   string
	asJSON bool
}

var registerOpts serveRegisterOptions
var manageOpts serveManageOptions
var logsOpts serveLogsOptions
var upgradeOpts serveUpgradeOptions
var upgradeStatusOpts serveUpgradeStatusOptions
var managedRegisterNative = managedRegister
var managedNativeStatusRead = managedNativeStatus
var managedPortOwned = servicemgr.OwnsListeningPort

type nativeServeStatus struct {
	Installed      bool
	Active         bool
	NativeState    string
	Server         daemon.ProcessIdentity
	Supervisor     daemon.ProcessIdentity
	ApplicationLog string
	IdentityError  string
}

type serveStatusOutput struct {
	Name              string             `json:"name"`
	Backend           servicemgr.Backend `json:"backend,omitempty"`
	Registered        bool               `json:"registered"`
	NativeState       string             `json:"native_state,omitempty"`
	Active            bool               `json:"active"`
	ServerPID         int                `json:"server_pid,omitempty"`
	SupervisorPID     int                `json:"supervisor_pid,omitempty"`
	ServerVerified    bool               `json:"server_verified"`
	PortOwned         bool               `json:"port_owned"`
	Health            string             `json:"health"`
	IdentityError     string             `json:"identity_error,omitempty"`
	RegisteredVersion string             `json:"registered_version,omitempty"`
	RunningVersion    string             `json:"running_version,omitempty"`
	VersionError      string             `json:"version_error,omitempty"`
	ConfigFile        string             `json:"config_file,omitempty"`
	Executable        string             `json:"executable,omitempty"`
	ApplicationLog    string             `json:"application_log,omitempty"`
}

func newServeManagementCommands(info buildinfo.Info) []*gcli.Command {
	register := &gcli.Command{Name: "register", Desc: "Register a managed native server service (no start by default)",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			c.StrOpt(&registerOpts.name, "name", "", servicemgr.DefaultName, "managed instance name")
			c.StrOpt(&registerOpts.exe, "exe", "", "", "server executable path (default current Gofer)")
			c.StrOpt(&registerOpts.workDir, "work-dir", "", "", "server working directory (default config directory)")
			c.StrOpt(&registerOpts.scope, "scope", "", "", "Linux system|user scope (default system)")
			c.StrOpt(&registerOpts.runAs, "run-as", "", "", "account for a Linux system service")
			c.BoolOpt(&registerOpts.elevated, "elevated", "", false, "register an elevated Windows task")
			c.BoolOpt(&registerOpts.adopt, "adopt", "", false, "adopt a verified legacy Gofer Windows task")
			c.BoolOpt(&registerOpts.start, "start", "", false, "start after registration")
			c.StrOpt(&registerOpts.addr, "addr", "", "", "non-secret listen address override")
			c.BoolOpt(&registerOpts.noWeb, "no-web", "", false, "disable embedded Web")
			c.StrOpt(&registerOpts.webDir, "web-dir", "", "", "on-disk Web directory")
			c.BoolOpt(&registerOpts.allowEmptyToken, "allow-empty-token", "", false, "allow server without an auth token")
		}, Func: func(c *gcli.Command, _ []string) error { return runServeRegister(c, info) }}
	start := &gcli.Command{Name: "start", Desc: "Start a registered native server", Config: bindManageName,
		Func: func(c *gcli.Command, _ []string) error { return runServeManagedAction(c, "start") }}
	restart := &gcli.Command{Name: "restart", Desc: "Stop then start a registered native server", Config: bindManageName,
		Func: func(c *gcli.Command, _ []string) error { return runServeManagedAction(c, "restart") }}
	uninstall := &gcli.Command{Name: "uninstall", Desc: "Stop and remove the native registration; keep server assets", Config: bindManageName,
		Func: func(c *gcli.Command, _ []string) error { return runServeManagedAction(c, "uninstall") }}
	status := &gcli.Command{Name: "status", Desc: "Inspect native registration, server identity and health", Config: func(c *gcli.Command) {
		bindManageName(c)
		c.BoolOpt(&manageOpts.asJSON, "json", "", false, "print JSON")
	}, Func: runServeManagedStatus}
	logs := &gcli.Command{Name: "logs", Desc: "Read the managed application log", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		c.StrOpt(&logsOpts.name, "name", "", servicemgr.DefaultName, "managed instance name")
		c.IntOpt(&logsOpts.lines, "lines", "n", 50, "last N lines (1..1000)")
		c.BoolOpt(&logsOpts.follow, "follow", "f", false, "follow appended lines")
		c.BoolOpt(&logsOpts.journal, "journal", "", false, "read Linux systemd journal instead")
	}, Func: runServeManagedLogs}
	upgradeStatus := &gcli.Command{Name: "status", Desc: "Read a durable upgrade result", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		c.StrOpt(&upgradeStatusOpts.name, "name", "", servicemgr.DefaultName, "managed instance name")
		c.BoolOpt(&upgradeStatusOpts.asJSON, "json", "", false, "print JSON")
		c.AddArg("upgrade_id", "durable upgrade id", true)
	}, Func: runServeUpgradeStatus}
	upgrade := &gcli.Command{Name: "upgrade", Desc: "Upgrade a registered server from a prebuilt binary", Config: func(c *gcli.Command) {
		bindConfigFlag(c)
		c.StrOpt(&upgradeOpts.name, "name", "", servicemgr.DefaultName, "managed instance name")
		c.StrOpt(&upgradeOpts.binary, "binary", "", "", "prebuilt candidate binary path")
		c.BoolOpt(&upgradeOpts.noWait, "no-wait", "", false, "return after durable drain acceptance")
	}, Subs: []*gcli.Command{upgradeStatus}, Func: runServeUpgrade}
	return []*gcli.Command{register, start, restart, uninstall, status, logs, upgrade}
}

func bindManageName(c *gcli.Command) {
	bindConfigFlag(c)
	c.StrOpt(&manageOpts.name, "name", "", servicemgr.DefaultName, "managed instance name")
}

func managementContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 190*time.Second)
}

func managerFor(name string) (*servicemgr.Manager, error) {
	if name == "" {
		name = servicemgr.DefaultName
	}
	dir, err := config.ConfigDir()
	if err != nil {
		return nil, err
	}
	return servicemgr.NewManager(dir, name)
}

func runServeRegister(c *gcli.Command, info buildinfo.Info) error {
	if config.IsClientRunMode() {
		return clientModeRefusal("serve register requires local server configuration")
	}
	m, err := managerFor(registerOpts.name)
	if err != nil {
		return err
	}
	cfg, configFile, err := config.Load(config.InputCfgFile)
	if err != nil {
		return err
	}
	if configFile == "" {
		return errors.New("serve register requires an existing configuration file")
	}
	if fi, err := os.Stat(configFile); err != nil {
		return fmt.Errorf("service config file: %w", err)
	} else if fi.IsDir() {
		return errors.New("service config path is a directory")
	}
	exe := registerOpts.exe
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return err
		}
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	exe = filepath.Clean(exe)
	workDir := registerOpts.workDir
	if workDir == "" {
		workDir = m.ConfigDir
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return err
	}
	workDir = filepath.Clean(workDir)
	webDir := registerOpts.webDir
	if webDir != "" {
		webDir, err = config.ResolveLocalPathAt("--web-dir", webDir, m.ConfigDir)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(webDir) {
			webDir = filepath.Join(workDir, webDir)
		}
		webDir = filepath.Clean(webDir)
	}
	spec := servicemgr.Spec{SchemaVersion: servicemgr.SpecSchema, Name: m.Name, Exe: exe, WorkDir: workDir,
		ConfigFile: filepath.Clean(configFile), ConfigDir: m.ConfigDir,
		RuntimeDir: filepath.Join(filepath.Dir(configFile), "run"),
		Serve: servicemgr.ServeOptions{Addr: registerOpts.addr, NoWeb: registerOpts.noWeb,
			WebDir: webDir, AllowEmptyToken: registerOpts.allowEmptyToken}}
	if err := managedRegisterSpec(registerOpts, &spec); err != nil {
		return err
	}
	if self, err := daemon.CurrentProcessIdentity(); err == nil && daemon.SameExecutable(self.Exe, spec.Exe) {
		spec.RegisteredVersion = info.DisplayVersion()
	}
	if err := spec.CheckFiles(); err != nil {
		return err
	}
	if registerOpts.start {
		if err := preflightManagedAssets(cfg, spec); err != nil {
			return err
		}
	}
	ctx, cancel := managementContext()
	defer cancel()
	if err := managedRegisterNative(ctx, m, spec, registerOpts); err != nil {
		return err
	}
	if registerOpts.start {
		if err := waitManagedReady(ctx, m); err != nil {
			return err
		}
	}
	c.Printf("registered %s (%s)\n", m.Name, spec.Backend)
	if registerOpts.start {
		c.Printf("started %s\n", m.Name)
	}
	return nil
}

func preflightManagedAssets(cfg *config.Config, spec servicemgr.Spec) error {
	if !spec.Serve.AllowEmptyToken && cfg.Server.Token == "" && (cfg.Server.TokenEnv == "" || os.Getenv(cfg.Server.TokenEnv) == "") {
		return errors.New("managed server would start without a token; set server.token/token_env or --allow-empty-token")
	}
	if cfg.Server.TLS != nil && cfg.Server.TLS.Addr != "" {
		if cfg.Server.TLS.CertFile == "" || cfg.Server.TLS.KeyFile == "" {
			return errors.New("managed TLS requires both certificate and key files")
		}
		cert, err := config.ResolveLocalPathAt("server.tls.cert_file", cfg.Server.TLS.CertFile, spec.ConfigDir)
		if err != nil {
			return err
		}
		key, err := config.ResolveLocalPathAt("server.tls.key_file", cfg.Server.TLS.KeyFile, spec.ConfigDir)
		if err != nil {
			return err
		}
		if !filepath.IsAbs(cert) {
			cert = filepath.Join(spec.WorkDir, cert)
		}
		if !filepath.IsAbs(key) {
			key = filepath.Join(spec.WorkDir, key)
		}
		if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
			return fmt.Errorf("managed TLS certificate/key: %w", err)
		}
	}
	if !spec.Serve.NoWeb {
		webDir := spec.Serve.WebDir
		if webDir == "" {
			webDir = cfg.Server.WebDir
		}
		if webDir != "" {
			resolved, err := config.ResolveLocalPathAt("server.web_dir", webDir, spec.ConfigDir)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(spec.WorkDir, resolved)
			}
			info, err := os.Stat(resolved)
			if err != nil {
				return fmt.Errorf("managed Web directory: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("managed Web path is not a directory: %s", resolved)
			}
			if _, err := os.ReadDir(resolved); err != nil {
				return fmt.Errorf("managed Web directory: %w", err)
			}
		}
	}
	return nil
}

func runServeManagedAction(c *gcli.Command, action string) error {
	m, err := managerFor(manageOpts.name)
	if err != nil {
		return err
	}
	if _, err := m.LoadSpec(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s is not registered; run serve register", m.Name)
		}
		return err
	}
	ctx, cancel := managementContext()
	defer cancel()
	switch action {
	case "start":
		err = managedStart(ctx, m)
	case "restart":
		err = managedRestart(ctx, m)
	case "uninstall":
		err = managedUninstall(ctx, m)
	default:
		return errors.New("unknown managed action")
	}
	if err != nil {
		return err
	}
	if action == "start" || action == "restart" {
		if err := waitManagedReady(ctx, m); err != nil {
			return err
		}
	}
	c.Printf("%s %s\n", action, m.Name)
	return nil
}

func waitManagedReady(ctx context.Context, m *servicemgr.Manager) error {
	readyCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var last serveStatusOutput
	var lastErr error
	for {
		last, lastErr = inspectManagedStatus(readyCtx, m)
		if lastErr == nil && last.ServerVerified && last.PortOwned && last.Health == "healthy" {
			return nil
		}
		select {
		case <-readyCtx.Done():
			return fmt.Errorf("managed service %s did not become verified and healthy: status=%s identity=%s: %w: %v", m.Name, last.Health, last.IdentityError, readyCtx.Err(), lastErr)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func runServeManagedStatus(c *gcli.Command, _ []string) error {
	m, err := managerFor(manageOpts.name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := inspectManagedStatus(ctx, m)
	if err != nil {
		return err
	}
	if manageOpts.asJSON {
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	c.Printf("name=%s registered=%t native=%s active=%t server_pid=%d verified=%t port_owned=%t health=%s registered_version=%s running_version=%s\n",
		out.Name, out.Registered, out.NativeState, out.Active, out.ServerPID, out.ServerVerified,
		out.PortOwned, out.Health, out.RegisteredVersion, out.RunningVersion)
	if out.IdentityError != "" {
		c.Printf("identity: %s\n", out.IdentityError)
	}
	if out.VersionError != "" {
		c.Printf("running version: %s\n", out.VersionError)
	}
	return nil
}

func inspectManagedStatus(ctx context.Context, m *servicemgr.Manager) (serveStatusOutput, error) {
	out := serveStatusOutput{Name: m.Name, Health: "unverified"}
	spec, err := m.LoadSpec()
	if errors.Is(err, os.ErrNotExist) {
		exists, nativeErr := managedNativeExists(ctx, m)
		if nativeErr != nil {
			return out, nativeErr
		}
		if exists {
			return out, errors.New("same-name native registration exists without a matching Gofer spec")
		}
		return out, nil
	}
	if err != nil {
		return out, err
	}
	native, err := managedNativeStatusRead(ctx, m)
	if errors.Is(err, os.ErrNotExist) {
		out.Backend, out.ConfigFile, out.Executable = spec.Backend, spec.ConfigFile, spec.Exe
		out.IdentityError = "Gofer spec exists but native registration is missing"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Backend, out.Registered, out.NativeState, out.Active = spec.Backend, native.Installed, native.NativeState, native.Active
	out.ConfigFile, out.Executable, out.RegisteredVersion = spec.ConfigFile, spec.Exe, spec.RegisteredVersion
	out.ServerPID, out.SupervisorPID = native.Server.PID, native.Supervisor.PID
	out.ServerVerified = native.Server.PID > 0 && native.IdentityError == ""
	out.IdentityError = native.IdentityError
	out.ApplicationLog = native.ApplicationLog
	if out.ApplicationLog == "" {
		out.ApplicationLog, err = managedApplicationLog(spec)
		if err != nil {
			return out, err
		}
	}
	if !out.Registered || !out.Active || !out.ServerVerified {
		return out, nil
	}
	configValue, _, err := config.Load(spec.ConfigFile)
	if err != nil {
		return out, err
	}
	addr := spec.Serve.Addr
	if addr == "" {
		addr = configValue.Server.Addr
	}
	host, rawPort, err := net.SplitHostPort(addr)
	if err != nil {
		out.IdentityError = "invalid configured listen address"
		return out, nil
	}
	port, err := strconv.ParseUint(rawPort, 10, 16)
	if err != nil || port == 0 {
		out.IdentityError = "invalid configured listen port"
		return out, nil
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	if host == "::" {
		host = "::1"
	}
	owned, ownerErr := managedPortOwned(out.ServerPID, host, uint16(port))
	if ownerErr != nil {
		out.IdentityError = ownerErr.Error()
		return out, nil
	}
	out.PortOwned = owned
	if !owned {
		out.IdentityError = "configured port is not owned by the verified server PID"
		return out, nil
	}
	probeHost := host
	if host != "127.0.0.1" && host != "::1" {
		probeHost = host
	}
	probeCtx, done := context.WithTimeout(ctx, 2*time.Second)
	defer done()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, "http://"+net.JoinHostPort(probeHost, rawPort)+"/health", nil)
	if err != nil {
		return out, err
	}
	client := &http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		out.Health = "unreachable"
		return out, nil
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		confirmed, confirmErr := managedNativeStatusRead(ctx, m)
		if confirmErr != nil || confirmed.Server != native.Server || !confirmed.Active {
			out.IdentityError = "managed process changed during health probe"
			return out, nil
		}
		ownedAgain, ownErr := managedPortOwned(out.ServerPID, host, uint16(port))
		if ownErr != nil || !ownedAgain {
			out.IdentityError = "managed port ownership changed during health probe"
			return out, nil
		}
		out.Health = "healthy"
		version, versionErr := readManagedRuntimeVersion(probeCtx, client, probeHost, rawPort, configValue)
		if versionErr != nil {
			out.VersionError = versionErr.Error()
		} else {
			out.RunningVersion = version
		}
		// A foreign process may take the port between /health and /v1/stats.
		// Only attach the returned version while the original native identity
		// and socket ownership still match after the version response.
		confirmed, confirmErr = managedNativeStatusRead(ctx, m)
		ownedAgain, ownErr = managedPortOwned(out.ServerPID, host, uint16(port))
		if confirmErr != nil || confirmed.Server != native.Server || !confirmed.Active || ownErr != nil || !ownedAgain {
			out.Health = "unverified"
			out.RunningVersion = ""
			out.VersionError = "managed identity or port changed during version query"
		}
	} else {
		out.Health = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	return out, nil
}

func readManagedRuntimeVersion(ctx context.Context, client *http.Client, host, port string, cfg *config.Config) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/v1/stats", nil)
	if err != nil {
		return "", err
	}
	// A CLI launched inside a job uses only its own job credential; it must not
	// load a server administrator token from the machine's dotenv file.
	token := os.Getenv(config.EnvJobToken)
	if token == "" {
		if cfg.Server.TokenEnv != "" {
			token = os.Getenv(cfg.Server.TokenEnv)
		}
		if token == "" {
			token = cfg.Server.Token
		}
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("stats unavailable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("stats returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode stats version: %w", err)
	}
	if payload.Version == "" {
		return "", errors.New("stats has no build version")
	}
	return payload.Version, nil
}

func managedApplicationLog(spec servicemgr.Spec) (string, error) {
	cfg, _, err := config.Load(spec.ConfigFile)
	if err != nil {
		return "", err
	}
	if cfg.Log.File != "" {
		path, err := config.ResolveLocalPathAt("log.file", cfg.Log.File, spec.ConfigDir)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(spec.WorkDir, path)
		}
		return filepath.Clean(path), nil
	}
	if cfg.Log.Dir != "" {
		dir, err := config.ResolveLocalPathAt("log.dir", cfg.Log.Dir, spec.ConfigDir)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(spec.WorkDir, dir)
		}
		return filepath.Join(dir, "serve.log"), nil
	}
	return filepath.Join(spec.RuntimeDir, "serve.log"), nil
}
