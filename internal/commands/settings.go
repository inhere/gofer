package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/errorx"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
)

// settings.go implements `gofer settings ls|get|set|reset|export|import` — the CLI over
// the extension settings API (GET/PUT/DELETE /v1/settings[/{key}], the YAML export and
// the import with its dry-run preview). Settings live in the SERVER's database, so this
// group is a pure HTTP client (internal/client) exactly like `config --remote`: a client
// node changes the same values a `serve` reads.
//
// It is a TOP-LEVEL resource group next to `config` (G033's core-resource list), not a
// `tool` subcommand: settings are a first-class thing an operator reads and writes, and
// `gofer settings ls` is the discoverability entry to the whole key registry.

// settingsExitErr is the process exit code of a failed `settings` command. Coded
// (errorx.ErrorCoder) because gcli derives a non-zero exit only from coded errors —
// without it a script cannot tell a refused write from a good one.
const settingsExitErr = 1

// settingsFail is the coded failure every `settings` command returns. The server's own
// words (an out-of-range int, a regex that does not compile, a prompt over 32 KiB) are
// the diagnosis, so nothing here rewords or hides them.
func settingsFail(format string, a ...any) error {
	return errorx.Failf(settingsExitErr, format, a...)
}

// Command option holders. They are package variables (gcli binds pointers at Config
// time); main_test.go's flag registry check picks them up with the other *Opts values.
var settingsLsOpts = struct {
	jsonOut bool
}{}

var settingsGetOpts = struct {
	jsonOut bool
}{}

var settingsSetOpts = struct {
	file string
}{}

var settingsExportOpts = struct {
	out string
}{}

var settingsImportOpts = struct {
	yes bool
}{}

// NewSettingsCmd builds the `settings` group. Every subcommand binds `-c/--config` and
// the server/token flags through the shared helpers (G011), so it talks to the same
// server as the rest of the CLI.
func NewSettingsCmd() *gcli.Command {
	ls := &gcli.Command{
		Name:    "ls",
		Aliases: []string{"list"},
		Desc:    "List every registered setting (key, type, whether it was changed, current value)",
		Help: `The keys are whatever this server registers: the noise-reduction thresholds and
exclusion lists, and the prompt overrides. "changed" means a value is stored for the
key; an unchanged key follows the binary's default.`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&settingsLsOpts.jsonOut, "json", "", false, "print the full entries as JSON instead of a table")
		},
		Func: runSettingsLs,
	}
	get := &gcli.Command{
		Name: "get",
		Desc: "Print one setting's current value",
		Help: `Prints the effective value: a string as it is, a list one item per line, a number
or bool as written. Use --json for the whole entry (default, type, bounds, who changed it).`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&settingsGetOpts.jsonOut, "json", "", false, "print the whole entry as JSON")
			c.AddArg("key", "setting key, e.g. work.draft.min_prompts", true)
		},
		Func: runSettingsGet,
	}
	set := &gcli.Command{
		Name: "set",
		Desc: "Write one setting (value argument, or --file for a multi-line value)",
		Help: `The value is converted to the key's registered type, so a list key takes a
comma-separated value and an int key a number:

  gofer settings set work.draft.min_prompts 3
  gofer settings set work.draft.exclude_projects "demo,sandbox"
  gofer settings set prompt.work.summarizer --file prompt.md
  cat prompt.md | gofer settings set prompt.work.summarizer --file -

The server validates the value and refuses (non-zero exit) with its reason.`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.StrOpt(&settingsSetOpts.file, "file", "f", "", "read the value from this file ('-' reads stdin), for multi-line prompts")
			c.AddArg("key", "setting key", true)
			c.AddArg("value", "the value (lists: comma-separated)", false)
		},
		Func: runSettingsSet,
	}
	reset := &gcli.Command{
		Name: "reset",
		Desc: "Drop a stored value, so the registered default applies again",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.AddArg("key", "setting key", true)
		},
		Func: runSettingsReset,
	}
	export := &gcli.Command{
		Name: "export",
		Desc: "Print the changed settings as YAML (for a backup or another machine)",
		Help: `Only the CHANGED keys are exported: a backup then carries what the operator
decided, while unchanged keys keep following the binary's defaults.`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.StrOpt(&settingsExportOpts.out, "out", "o", "", "write the YAML to this file instead of stdout")
		},
		Func: runSettingsExport,
	}
	imp := &gcli.Command{
		Name: "import",
		Desc: "Apply a YAML settings file (dry run: shows every key's change first)",
		Help: `Every key is reported: set / reset / unchanged, and the reason a refused key was
refused. WITHOUT --yes nothing is written (the server runs the same validation in its
dry-run mode), so the default is a safe preview:

  gofer settings import backup.yaml            # preview
  gofer settings import backup.yaml --yes      # write
  gofer settings export | gofer settings import - --yes`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.BoolOpt(&settingsImportOpts.yes, "yes", "y", false, "write the values instead of printing the preview")
			c.AddArg("file", "YAML file with key: value lines (`-` for stdin)", true)
		},
		Func: runSettingsImport,
	}
	return &gcli.Command{
		Name: "settings",
		Desc: "Read and write the server's extension settings (thresholds, exclusion lists, prompt overrides)",
		Subs: []*gcli.Command{ls, get, set, reset, export, imp},
	}
}

// runSettingsLs runs `gofer settings ls`.
func runSettingsLs(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return settingsFail("%v", err)
	}
	items, err := cli.ListSettings()
	if err != nil {
		return settingsFail("%v", err)
	}
	if settingsLsOpts.jsonOut {
		return printSettingsJSON(c, items)
	}
	width := 0
	for _, it := range items {
		if len(it.Key) > width {
			width = len(it.Key)
		}
	}
	if width < 20 {
		width = 20
	}
	c.Printf("%-*s %-9s %-8s %s\n", width, "KEY", "TYPE", "CHANGED", "VALUE")
	for _, it := range items {
		c.Printf("%-*s %-9s %-8s %s\n", width, it.Key, it.Kind, settingsModifiedLabel(it.Modified), settingsValueSummary(it.Value, 48))
	}
	return nil
}

// runSettingsGet runs `gofer settings get <key>`.
func runSettingsGet(c *gcli.Command, args []string) error {
	key := toolArg(c, args, 0, "key")
	if key == "" {
		return settingsFail("settings get requires a <key> argument (see `gofer settings ls`)")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return settingsFail("%v", err)
	}
	item, err := cli.GetSetting(key)
	if err != nil {
		return settingsFail("%v", err)
	}
	if settingsGetOpts.jsonOut {
		return printSettingsJSON(c, item)
	}
	printSettingValue(c, item.Value)
	return nil
}

// runSettingsSet runs `gofer settings set <key> [value]` / `--file`.
func runSettingsSet(c *gcli.Command, args []string) error {
	key := toolArg(c, args, 0, "key")
	if key == "" {
		return settingsFail("settings set requires a <key> argument (see `gofer settings ls`)")
	}
	raw, err := settingsSetRaw(c, args)
	if err != nil {
		return err
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return settingsFail("%v", err)
	}
	// The registered kind decides how the text becomes a JSON value: an int key must be
	// sent as a number and a list key as an array, or the server's type check refuses it.
	item, err := cli.GetSetting(key)
	if err != nil {
		return settingsFail("%v", err)
	}
	value, err := settingsParseValue(item.Kind, raw)
	if err != nil {
		return settingsFail("%s: %v", key, err)
	}
	item, err = cli.SetSetting(key, value)
	if err != nil {
		return settingsFail("%v", err)
	}
	if item.Multiline {
		// A prompt is too long for one line: report its size instead of dumping it back.
		c.Printf("set %s (%d characters)\n", item.Key, settingsValueLen(item.Value))
		return nil
	}
	c.Printf("set %s = %s\n", item.Key, settingsValueSummary(item.Value, 200))
	return nil
}

// settingsSetRaw reads the value to write: --file (or stdin with `-`) wins over the
// positional argument, so a multi-line prompt never has to go through the shell.
func settingsSetRaw(c *gcli.Command, args []string) (string, error) {
	path := strings.TrimSpace(settingsSetOpts.file)
	if path == "" {
		if len(args) > 1 {
			if v := args[1]; strings.TrimSpace(v) != "" {
				return v, nil
			}
		}
		v := strings.TrimSpace(argString(c, "value"))
		if v == "" {
			return "", settingsFail("settings set requires a value argument or --file <path>")
		}
		return v, nil
	}
	data, err := readSettingsInput(path)
	if err != nil {
		return "", settingsFail("read %s: %v", path, err)
	}
	// One trailing newline is how an editor ends a text file; it is not part of the
	// prompt the operator meant to write.
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), nil
}

// runSettingsReset runs `gofer settings reset <key>`.
func runSettingsReset(c *gcli.Command, args []string) error {
	key := toolArg(c, args, 0, "key")
	if key == "" {
		return settingsFail("settings reset requires a <key> argument (see `gofer settings ls`)")
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return settingsFail("%v", err)
	}
	res, err := cli.ResetSetting(key)
	if err != nil {
		return settingsFail("%v", err)
	}
	if !res.Reset {
		c.Printf("%s already follows the default (%s)\n", key, settingsValueSummary(res.Value, 200))
		return nil
	}
	c.Printf("reset %s → default %s\n", key, settingsValueSummary(res.Value, 200))
	return nil
}

// runSettingsExport runs `gofer settings export [-o file]`.
func runSettingsExport(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return settingsFail("%v", err)
	}
	doc, err := cli.ExportSettings()
	if err != nil {
		return settingsFail("%v", err)
	}
	if out := strings.TrimSpace(settingsExportOpts.out); out != "" {
		if err := os.WriteFile(out, doc, 0o644); err != nil {
			return settingsFail("write %s: %v", out, err)
		}
		c.Printf("wrote %s (%d bytes)\n", out, len(doc))
		return nil
	}
	c.Print(string(doc))
	return nil
}

// runSettingsImport runs `gofer settings import <file|-> [--yes]`. Without --yes the
// server previews the import (its dry-run validates with the same rules), so the
// default is safe: nothing is written until the operator has seen every key.
func runSettingsImport(c *gcli.Command, args []string) error {
	path := toolArg(c, args, 0, "file")
	if path == "" {
		return settingsFail("settings import requires a <file> argument (`-` for stdin)")
	}
	doc, err := readSettingsInput(path)
	if err != nil {
		return settingsFail("read %s: %v", path, err)
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return settingsFail("%v", err)
	}
	res, err := cli.ImportSettings(doc, !settingsImportOpts.yes)
	if err != nil {
		return settingsFail("%v", err)
	}
	printSettingsChanges(c, res)
	if res.Errors > 0 && settingsImportOpts.yes {
		return settingsFail("%d key(s) were refused; the rest were written", res.Errors)
	}
	if res.Errors > 0 {
		return settingsFail("%d key(s) would be refused", res.Errors)
	}
	return nil
}

// printSettingsChanges renders an import result key by key: set / reset / unchanged with
// the old and new value, and the reason for a refused key.
func printSettingsChanges(c *gcli.Command, res client.SettingImport) {
	if len(res.Changes) == 0 {
		c.Println("nothing to import: the document named no known setting")
		return
	}
	changed, refused := 0, 0
	for _, ch := range res.Changes {
		if ch.Error != "" {
			refused++
			c.Printf("%-9s %-34s rejected: %s\n", ch.Action, ch.Key, ch.Error)
			continue
		}
		switch ch.Action {
		case "set":
			changed++
			c.Printf("%-9s %-34s %s → %s\n", ch.Action, ch.Key, settingsValueSummary(ch.Old, 40), settingsValueSummary(ch.New, 40))
		case "reset":
			changed++
			c.Printf("%-9s %-34s %s → (default)\n", ch.Action, ch.Key, settingsValueSummary(ch.Old, 40))
		default:
			c.Printf("%-9s %-34s %s\n", ch.Action, ch.Key, settingsValueSummary(ch.New, 40))
		}
	}
	if res.DryRun {
		c.Printf("\ndry run: %d key(s) would change, %d refused — nothing written (pass --yes to apply)\n", changed, refused)
		return
	}
	c.Printf("\napplied: %d key(s) written, %d refused\n", changed, refused)
}

// readSettingsInput reads a settings input file, or stdin for `-`.
func readSettingsInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(os.Stdin, 8<<20))
	}
	return os.ReadFile(path)
}

// settingsParseValue converts the command line text into the JSON value the key's
// registered kind expects. A list accepts a comma-separated value (`--item` repeats are
// not used: one spelling, documented in the help).
func settingsParseValue(kind, raw string) (any, error) {
	switch strings.ToLower(kind) {
	case "int":
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", raw)
		}
		return n, nil
	case "bool":
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "yes", "on", "1":
			return true, nil
		case "false", "no", "off", "0":
			return false, nil
		}
		return nil, fmt.Errorf("%q is not a bool (true|false)", raw)
	case "strings", "patterns":
		out := make([]string, 0, 4)
		for _, part := range strings.Split(raw, ",") {
			if v := strings.TrimSpace(part); v != "" {
				out = append(out, v)
			}
		}
		return out, nil
	default:
		return raw, nil
	}
}

// printSettingValue prints a value the way a shell script wants it: a list one item per
// line, everything else as written.
func printSettingValue(c *gcli.Command, v any) {
	switch val := v.(type) {
	case []string:
		for _, item := range val {
			c.Println(item)
		}
	case []any:
		for _, item := range val {
			c.Println(settingsValueSummary(item, 0))
		}
	case string:
		c.Println(val)
	case nil:
		c.Println("")
	default:
		c.Println(settingsValueSummary(val, 0))
	}
}

// settingsValueLen is the character count of a value, as a prompt's write confirmation
// reports it.
func settingsValueLen(v any) int {
	if s, ok := v.(string); ok {
		return len(s)
	}
	return len(settingsValueText(v))
}

// settingsValueSummary renders a value for one table cell: a list joins on commas, a
// prompt's newlines become a visible marker, and anything longer than limit is cut.
func settingsValueSummary(v any, limit int) string {
	s := settingsValueText(v)
	if limit > 0 && len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}

// settingsValueText renders a value on one line.
func settingsValueText(v any) string {
	var s string
	switch val := v.(type) {
	case nil:
		s = ""
	case []string:
		s = strings.Join(val, ",")
	case []any:
		parts := make([]string, 0, len(val))
		for _, item := range val {
			parts = append(parts, fmt.Sprintf("%v", item))
		}
		s = strings.Join(parts, ",")
	case bool:
		s = strconv.FormatBool(val)
	case float64:
		s = strconv.FormatFloat(val, 'f', -1, 64)
	default:
		s = fmt.Sprintf("%v", val)
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", `\n`)
	return s
}

// settingsModifiedLabel is the CHANGED column: a stored value, or the default.
func settingsModifiedLabel(modified bool) string {
	if modified {
		return "yes"
	}
	return ""
}

// printSettingsJSON prints v as indented JSON (the `--json` spelling of ls/get).
func printSettingsJSON(c *gcli.Command, v any) error {
	enc, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return settingsFail("encode json: %v", err)
	}
	c.Println(string(enc))
	return nil
}
