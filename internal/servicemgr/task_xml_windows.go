//go:build windows

package servicemgr

import (
	"encoding/xml"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const taskOwnershipPrefix = "gofer-managed:v1:"

type taskDocument struct {
	XMLName      xml.Name `xml:"Task"`
	XMLNS        string   `xml:"xmlns,attr"`
	Version      string   `xml:"version,attr"`
	Registration struct {
		Description string `xml:"Description"`
		URI         string `xml:"URI"`
	} `xml:"RegistrationInfo"`
	Triggers struct {
		Logon struct {
			Enabled bool   `xml:"Enabled"`
			UserID  string `xml:"UserId"`
		} `xml:"LogonTrigger"`
	} `xml:"Triggers"`
	Principals struct {
		Principal struct {
			ID        string `xml:"id,attr"`
			UserID    string `xml:"UserId"`
			LogonType string `xml:"LogonType"`
			RunLevel  string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		MultipleInstances  string `xml:"MultipleInstancesPolicy"`
		DisallowBattery    bool   `xml:"DisallowStartIfOnBatteries"`
		StopOnBattery      bool   `xml:"StopIfGoingOnBatteries"`
		AllowHardTerminate bool   `xml:"AllowHardTerminate"`
		StartWhenAvailable bool   `xml:"StartWhenAvailable"`
		ExecutionLimit     string `xml:"ExecutionTimeLimit"`
		Restart            struct {
			Interval string `xml:"Interval"`
			Count    int    `xml:"Count"`
		} `xml:"RestartOnFailure"`
	} `xml:"Settings"`
	Actions struct {
		Context string `xml:"Context,attr"`
		Exec    struct {
			Command          string `xml:"Command"`
			Arguments        string `xml:"Arguments"`
			WorkingDirectory string `xml:"WorkingDirectory"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

func taskDescription(spec Spec) string {
	return taskOwnershipPrefix + spec.Name + ":" + spec.Owner + ":" + spec.ConfigDir
}

func (m *Manager) SupervisorPath() string {
	return filepath.Join(m.serviceDir(), m.Name+"-supervisor.exe")
}

func makeTaskDocument(spec Spec, supervisorPath, specPath string) (taskDocument, error) {
	if err := spec.Validate(); err != nil {
		return taskDocument{}, err
	}
	if spec.Backend != BackendWindowsTask {
		return taskDocument{}, fmt.Errorf("backend %q is not a Windows task", spec.Backend)
	}
	doc := taskDocument{XMLNS: "http://schemas.microsoft.com/windows/2004/02/mit/task", Version: "1.4"}
	doc.Registration.Description = taskDescription(spec)
	doc.Registration.URI = `\` + spec.Name
	doc.Triggers.Logon.Enabled = true
	doc.Triggers.Logon.UserID = spec.Owner
	doc.Principals.Principal.ID = "Author"
	doc.Principals.Principal.UserID = spec.Owner
	doc.Principals.Principal.LogonType = "InteractiveToken"
	doc.Principals.Principal.RunLevel = "LeastPrivilege"
	if spec.Elevated {
		doc.Principals.Principal.RunLevel = "HighestAvailable"
	}
	doc.Settings.MultipleInstances = "IgnoreNew"
	doc.Settings.StartWhenAvailable = true
	doc.Settings.ExecutionLimit = "PT0S"
	doc.Settings.Restart.Interval = "PT1M"
	doc.Settings.Restart.Count = 3
	doc.Actions.Context = "Author"
	doc.Actions.Exec.Command = supervisorPath
	doc.Actions.Exec.Arguments = windows.ComposeCommandLine([]string{"serve", "supervise", "--spec", specPath})
	doc.Actions.Exec.WorkingDirectory = spec.WorkDir
	return doc, nil
}

func buildTaskXML(spec Spec, supervisorPath, specPath string) (string, error) {
	doc, err := makeTaskDocument(spec, supervisorPath, specPath)
	if err != nil {
		return "", err
	}
	data, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	// RegisterTask accepts a UTF-16 BSTR. An XML declaration claiming UTF-8
	// conflicts with that transport and Task Scheduler rejects it as malformed.
	return string(data), nil
}

func verifyOwnedTask(xmlText string, spec Spec, supervisorPath, specPath string) error {
	if err := verifySingleTaskActionAndTrigger(xmlText); err != nil {
		return err
	}
	var actual taskDocument
	if err := decodeTaskXML(xmlText, &actual); err != nil {
		return fmt.Errorf("decode existing task: %w", err)
	}
	wantLevel := "LeastPrivilege"
	if spec.Elevated {
		wantLevel = "HighestAvailable"
	}
	actualLevel := actual.Principals.Principal.RunLevel
	if actualLevel == "" {
		actualLevel = "LeastPrivilege"
	} // Scheduler omits the default.
	triggerOwner := actual.Triggers.Logon.UserID
	if sid, sidErr := windows.StringToSid(spec.Owner); sidErr == nil && triggerOwner != "" {
		account, domain, _, lookupErr := sid.LookupAccount("")
		if lookupErr == nil && strings.EqualFold(triggerOwner, domain+`\`+account) {
			triggerOwner = spec.Owner
		}
	}
	if actual.Registration.Description != taskDescription(spec) || actual.Registration.URI != `\`+spec.Name ||
		actual.Principals.Principal.ID != "Author" || actual.Actions.Context != "Author" ||
		!strings.EqualFold(actual.Actions.Exec.Command, supervisorPath) ||
		actual.Actions.Exec.Arguments != windows.ComposeCommandLine([]string{"serve", "supervise", "--spec", specPath}) ||
		actual.Actions.Exec.WorkingDirectory != spec.WorkDir ||
		actual.Principals.Principal.UserID != spec.Owner || actual.Principals.Principal.LogonType != "InteractiveToken" ||
		actualLevel != wantLevel || triggerOwner != spec.Owner {
		return fmt.Errorf("task %q is not this managed instance: description=%v uri=%v command=%v arguments=%v workdir=%v owner=%v logon=%v level=%v trigger=%v",
			spec.Name, actual.Registration.Description == taskDescription(spec), actual.Registration.URI == `\`+spec.Name,
			strings.EqualFold(actual.Actions.Exec.Command, supervisorPath),
			actual.Actions.Exec.Arguments == windows.ComposeCommandLine([]string{"serve", "supervise", "--spec", specPath}),
			actual.Actions.Exec.WorkingDirectory == spec.WorkDir, actual.Principals.Principal.UserID == spec.Owner,
			actual.Principals.Principal.LogonType == "InteractiveToken", actualLevel == wantLevel,
			triggerOwner == spec.Owner)
	}
	return nil
}

func verifyAdoptableLegacyTask(xmlText string, spec Spec) error {
	var actual taskDocument
	if err := decodeTaskXML(xmlText, &actual); err != nil {
		return err
	}
	if err := verifySingleTaskActionAndTrigger(xmlText); err != nil {
		return err
	}
	args, err := windows.DecomposeCommandLine(actual.Actions.Exec.Arguments)
	if err != nil {
		return err
	}
	// The only recognized old topology is scripts/start.ps1's scheduled action:
	// conhost --headless pwsh -File <repo>/scripts/win-supervisor.ps1, or
	// direct pwsh/powershell -File on hosts without headless conhost.
	cmdBase := strings.ToLower(filepath.Base(actual.Actions.Exec.Command))
	if cmdBase == "conhost.exe" {
		if len(args) < 3 || args[0] != "--headless" {
			return fmt.Errorf("task %q has unknown legacy action", spec.Name)
		}
		pwshBase := strings.ToLower(filepath.Base(args[1]))
		if pwshBase != "pwsh.exe" && pwshBase != "powershell.exe" {
			return fmt.Errorf("task %q has unknown legacy shell", spec.Name)
		}
		args = args[2:]
	} else if cmdBase != "pwsh.exe" && cmdBase != "powershell.exe" {
		return fmt.Errorf("task %q has unknown legacy action", spec.Name)
	} else {
		if len(args) == 0 || args[0] != "-WindowStyle" || len(args) < 2 || args[1] != "Hidden" {
			return fmt.Errorf("task %q has unknown legacy shell options", spec.Name)
		}
		args = args[2:]
	}
	if len(args) != 14 || args[0] != "-NoProfile" || args[1] != "-NonInteractive" ||
		args[2] != "-ExecutionPolicy" || args[3] != "Bypass" || args[4] != "-File" ||
		args[6] != "-ExeDir" || args[8] != "-WorkDir" || args[10] != "-ServeArgs" || args[12] != "-EnvExtra" {
		return fmt.Errorf("task %q has unknown or repeated legacy arguments", spec.Name)
	}
	flags := map[string]string{"-file": args[5], "-exedir": args[7], "-workdir": args[9], "-serveargs": args[11], "-envextra": args[13]}
	wantScript := filepath.Join(filepath.Dir(filepath.Dir(spec.Exe)), "scripts", "win-supervisor.ps1")
	serveArgs := strings.Split(flags["-serveargs"], ",")
	if len(serveArgs) == 0 || serveArgs[0] != "serve" {
		return fmt.Errorf("task %q has no legacy serve command", spec.Name)
	}
	legacyConfig := filepath.Join(spec.ConfigDir, "config.yaml")
	for i := 1; i < len(serveArgs); i++ {
		switch serveArgs[i] {
		case "--no-web":
		case "--web-dir", "--addr", "--config", "-c":
			if i+1 >= len(serveArgs) || serveArgs[i+1] == "" {
				return fmt.Errorf("task %q has invalid legacy serve args", spec.Name)
			}
			if serveArgs[i] == "--config" || serveArgs[i] == "-c" {
				legacyConfig = serveArgs[i+1]
			}
			i++
		default:
			return fmt.Errorf("task %q has unknown legacy serve args", spec.Name)
		}
	}
	wantLevel := "LeastPrivilege"
	if spec.Elevated {
		wantLevel = "HighestAvailable"
	}
	actualLevel := actual.Principals.Principal.RunLevel
	if actualLevel == "" {
		actualLevel = "LeastPrivilege"
	}
	wantUser := spec.Owner
	if sid, sidErr := windows.StringToSid(spec.Owner); sidErr == nil {
		account, domain, _, lookupErr := sid.LookupAccount("")
		if lookupErr == nil && strings.EqualFold(actual.Principals.Principal.UserID, domain+`\`+account) {
			wantUser = actual.Principals.Principal.UserID
		}
	}
	if actual.Registration.URI != `\`+spec.Name ||
		actual.Principals.Principal.ID != "Author" || actual.Actions.Context != "Author" ||
		!actual.Triggers.Logon.Enabled || actual.Actions.Exec.WorkingDirectory != spec.WorkDir ||
		!strings.EqualFold(filepath.Clean(flags["-file"]), wantScript) ||
		!strings.EqualFold(filepath.Clean(flags["-exedir"]), filepath.Dir(spec.Exe)) ||
		!strings.EqualFold(filepath.Clean(flags["-workdir"]), spec.WorkDir) ||
		!strings.EqualFold(strings.TrimPrefix(flags["-envextra"], "GOFER_CONFIG_DIR="), spec.ConfigDir) ||
		!strings.EqualFold(filepath.Clean(legacyConfig), spec.ConfigFile) ||
		!strings.EqualFold(actual.Principals.Principal.UserID, wantUser) ||
		!strings.EqualFold(actual.Triggers.Logon.UserID, wantUser) ||
		actual.Principals.Principal.LogonType != "InteractiveToken" || actualLevel != wantLevel {
		return fmt.Errorf("task %q is not a verified legacy Gofer task", spec.Name)
	}
	return nil
}

func verifySingleTaskActionAndTrigger(xmlText string) error {
	trimmed := strings.TrimSpace(xmlText)
	if strings.HasPrefix(trimmed, "<?xml") {
		end := strings.Index(trimmed, "?>")
		if end < 0 {
			return fmt.Errorf("invalid legacy XML declaration")
		}
		trimmed = trimmed[end+2:]
	}
	decoder := xml.NewDecoder(strings.NewReader(trimmed))
	var stack []string
	var triggers, actions, logons, execs, principals int
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 1 && t.Name.Local == "Triggers" {
				triggers++
			}
			if len(stack) == 1 && t.Name.Local == "Actions" {
				actions++
			}
			if len(stack) == 2 && stack[1] == "Principals" {
				principals++
			}
			if len(stack) == 2 && stack[1] == "Triggers" {
				if t.Name.Local != "LogonTrigger" {
					return fmt.Errorf("legacy task has extra trigger")
				}
				logons++
			}
			if len(stack) == 2 && stack[1] == "Actions" {
				if t.Name.Local != "Exec" {
					return fmt.Errorf("legacy task has extra action")
				}
				execs++
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if len(stack) == 0 {
				return fmt.Errorf("invalid legacy XML nesting")
			}
			stack = stack[:len(stack)-1]
		}
	}
	if triggers != 1 || actions != 1 || logons != 1 || execs != 1 || principals != 1 {
		return fmt.Errorf("task must have one principal, one logon trigger and one exec action")
	}
	return nil
}

func decodeTaskXML(xmlText string, doc *taskDocument) error {
	// Task Scheduler returns a UTF-16 declaration in its BSTR. bstrValue has
	// already decoded those code units to a Go UTF-8 string.
	trimmed := strings.TrimSpace(xmlText)
	if strings.HasPrefix(trimmed, "<?xml") {
		end := strings.Index(trimmed, "?>")
		if end < 0 {
			return fmt.Errorf("invalid Task Scheduler XML declaration")
		}
		trimmed = trimmed[end+2:]
	}
	return xml.Unmarshal([]byte(trimmed), doc)
}
