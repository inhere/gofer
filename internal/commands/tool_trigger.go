package commands

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/client"
	"github.com/inhere/gofer/internal/config"
	"github.com/inhere/gofer/internal/trigger"
)

// tool_trigger.go implements `gofer tool trigger ls|show|invoke` (G033: a small utility, so
// it lives under `gofer tool`). ls / show read the redacted trigger views over the
// authenticated API (trigger.read); invoke is the operator's self-test of the external
// endpoint: it signs the body with a secret the caller names (an environment variable or
// stdin, never an argument) and posts it without any gofer token. There is deliberately no
// way to add, edit or rotate a trigger here: triggers live in the server's config file.

// toolStdin is where the secret and the body come from for "-" / --secret-stdin. A var so
// tests can feed it.
var toolStdin io.Reader = os.Stdin

var toolTriggerShowOpts = struct {
	deliveries int
}{}

var toolTriggerInvokeOpts = struct {
	body            string
	id              string
	secretEnv       string
	secretStdin     bool
	headers         gcli.Strings
	timestampHeader string
	signatureHeader string
}{}

const (
	defaultTriggerTimestampHeader = "X-Gofer-Trigger-Timestamp"
	defaultTriggerSignatureHeader = "X-Gofer-Trigger-Signature"
)

// NewToolTriggerCmd builds the `tool trigger` group (registered by NewToolCmd).
func NewToolTriggerCmd() *gcli.Command {
	ls := &gcli.Command{
		Name:    "ls",
		Aliases: []string{"list"},
		Desc:    cliText("cli.tool.trigger.ls.desc"),
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
		},
		Func: runToolTriggerLs,
	}
	show := &gcli.Command{
		Name: "show",
		Desc: cliText("cli.tool.trigger.show.desc"),
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.IntOpt(&toolTriggerShowOpts.deliveries, "deliveries", "n", 5, cliText("cli.tool.trigger.show.deliveries_flag"))
			c.AddArg("name", cliText("cli.tool.trigger.name_arg"), true)
		},
		Func: runToolTriggerShow,
	}
	invoke := &gcli.Command{
		Name: "invoke",
		Desc: cliText("cli.tool.trigger.invoke.desc"),
		Help: cliText("cli.tool.trigger.invoke.help") + "\n\n" +
			"  GOFER_TRIGGER_SECRET=... gofer tool trigger invoke source-update --body event.json --id evt-1 --secret-env GOFER_TRIGGER_SECRET\n" +
			"  printf '%s' \"$SECRET\" | gofer tool trigger invoke source-update --body event.json --secret-stdin",
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.StrOpt(&toolTriggerInvokeOpts.body, "body", "b", "", cliText("cli.tool.trigger.invoke.body_flag"))
			c.StrOpt(&toolTriggerInvokeOpts.id, "id", "", "", cliText("cli.tool.trigger.invoke.id_flag"))
			c.StrOpt(&toolTriggerInvokeOpts.secretEnv, "secret-env", "", "", cliText("cli.tool.trigger.invoke.secret_env_flag"))
			c.BoolOpt(&toolTriggerInvokeOpts.secretStdin, "secret-stdin", "", false, cliText("cli.tool.trigger.invoke.secret_stdin_flag"))
			c.VarOpt(&toolTriggerInvokeOpts.headers, "header", "H", cliText("cli.tool.trigger.invoke.header_flag"))
			c.StrOpt(&toolTriggerInvokeOpts.timestampHeader, "timestamp-header", "", defaultTriggerTimestampHeader, cliText("cli.tool.trigger.invoke.timestamp_header_flag"))
			c.StrOpt(&toolTriggerInvokeOpts.signatureHeader, "signature-header", "", defaultTriggerSignatureHeader, cliText("cli.tool.trigger.invoke.signature_header_flag"))
			c.AddArg("name", cliText("cli.tool.trigger.name_arg"), true)
		},
		Func: runToolTriggerInvoke,
	}
	return &gcli.Command{
		Name: "trigger",
		Desc: cliText("cli.tool.trigger.desc"),
		Subs: []*gcli.Command{ls, show, invoke},
	}
}

func runToolTriggerLs(c *gcli.Command, _ []string) error {
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	views, err := cli.ListTriggers()
	if err != nil {
		return toolFail("%v", err)
	}
	if len(views) == 0 {
		c.Println(cliText("cli.tool.trigger.none"))
		return nil
	}
	var table strings.Builder
	tw := tabwriter.NewWriter(&table, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, strings.ReplaceAll(cliText("cli.tool.trigger.ls.header"), ",", "\t"))
	for _, v := range views {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", v.Name, v.Owner, v.Project, v.Template, triggerStateText(v), triggerLastText(v.Last))
	}
	if err := tw.Flush(); err != nil {
		return toolFail("%v", err)
	}
	c.Print(table.String())
	return nil
}

func runToolTriggerShow(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return toolFail("%s", cliText("cli.tool.trigger.name_required"))
	}
	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	v, err := cli.GetTrigger(name)
	if err != nil {
		return toolFail("%v", err)
	}
	c.Println(cliText("cli.tool.trigger.show.name", "name", v.Name, "state", triggerStateText(v)))
	c.Println(cliText("cli.tool.trigger.show.target", "owner", v.Owner, "project", v.Project, "template", v.Template, "provider", v.Provider))
	secret := cliText("cli.tool.trigger.show.secret_unset")
	if v.Auth.SecretConfigured {
		secret = cliText("cli.tool.trigger.show.secret_set")
	}
	c.Println(cliText("cli.tool.trigger.show.auth", "kind", v.Auth.Kind, "env", v.Auth.SecretEnv, "secret", secret,
		"ts", v.Auth.TimestampHeader, "sig", v.Auth.SignatureHeader, "window", v.Auth.ReplayWindowSec))
	dedupKey := v.Dedup.Key
	if dedupKey == "" {
		dedupKey = cliText("cli.tool.trigger.show.no_dedup_key")
	}
	c.Println(cliText("cli.tool.trigger.show.dedup", "key", dedupKey, "ttl", v.Dedup.TTLSec))
	c.Println(cliText("cli.tool.trigger.show.rate", "per_minute", v.Rate.PerMinute, "burst", v.Rate.Burst, "inflight", v.Rate.MaxInflight))
	c.Println(cliText("cli.tool.trigger.show.limits", "body", v.Limits.MaxBodyBytes, "var", v.Limits.MaxVarBytes))
	for _, is := range v.Issues {
		c.Println(cliText("cli.tool.trigger.show.issue", "reason", triggerIssueText(config.TriggerIssue{Code: is.Code, Arg: is.Arg})))
	}
	if toolTriggerShowOpts.deliveries <= 0 {
		return nil
	}
	rows, err := cli.TriggerDeliveries(name, toolTriggerShowOpts.deliveries)
	if err != nil {
		return toolFail("%v", err)
	}
	c.Println(cliText("cli.tool.trigger.show.deliveries", "count", len(rows)))
	for _, d := range rows {
		c.Println("  " + triggerLastText(&d) + triggerDeliveryTail(d))
	}
	return nil
}

func runToolTriggerInvoke(c *gcli.Command, args []string) error {
	name := toolArg(c, args, 0, "name")
	if name == "" {
		return toolFail("%s", cliText("cli.tool.trigger.name_required"))
	}
	opts := &toolTriggerInvokeOpts
	if strings.TrimSpace(opts.body) == "" {
		return toolFail("%s", cliText("cli.tool.trigger.invoke.body_required"))
	}
	if (opts.secretEnv != "") == opts.secretStdin {
		// Neither source, or both: the secret must come from exactly one place.
		return toolFail("%s", cliText("cli.tool.trigger.invoke.secret_source"))
	}
	if opts.secretStdin && strings.TrimSpace(opts.body) == "-" {
		return toolFail("%s", cliText("cli.tool.trigger.invoke.stdin_twice"))
	}
	secret, err := triggerSecret(opts.secretEnv, opts.secretStdin)
	if err != nil {
		return toolFail("%v", err)
	}
	body, err := readTriggerBody(strings.TrimSpace(opts.body))
	if err != nil {
		return toolFail("%v", err)
	}
	headers := map[string]string{}
	for _, h := range opts.headers {
		k, v, ok := strings.Cut(h, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return toolFail("%s", cliText("cli.tool.trigger.invoke.bad_header", "header", k))
		}
		headers[strings.TrimSpace(k)] = v
	}
	if opts.id != "" {
		headers["Idempotency-Key"] = opts.id
	}
	stamp := strconv.FormatInt(time.Now().Unix(), 10)
	headers[orDefaultStr(opts.timestampHeader, defaultTriggerTimestampHeader)] = stamp
	headers[orDefaultStr(opts.signatureHeader, defaultTriggerSignatureHeader)] = trigger.Sign(secret, stamp, body)

	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	res, err := cli.InvokeTrigger(name, body, headers)
	if err != nil {
		return toolFail("%v", err)
	}
	return printTriggerInvoke(c, res)
}

func printTriggerInvoke(c *gcli.Command, res client.TriggerInvokeResult) error {
	if res.Duplicate {
		c.Println(cliText("cli.tool.trigger.invoke.duplicate", "trigger", res.Trigger, "job", orDash(res.JobID), "status", orDash(res.Status), "request", res.RequestID))
		return nil
	}
	c.Println(cliText("cli.tool.trigger.invoke.accepted", "trigger", res.Trigger, "job", res.JobID, "request", res.RequestID))
	return nil
}

func orDefaultStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

// triggerSecret reads the signing secret from the named environment variable or from the
// first line of stdin. It never comes from a flag value and is never printed.
func triggerSecret(envName string, fromStdin bool) (string, error) {
	var secret string
	if fromStdin {
		line, err := bufio.NewReader(toolStdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("%s", cliText("cli.tool.trigger.invoke.secret_read"))
		}
		secret = strings.TrimRight(line, "\r\n")
	} else {
		secret = os.Getenv(strings.TrimSpace(envName))
	}
	if secret == "" {
		return "", fmt.Errorf("%s", cliText("cli.tool.trigger.invoke.secret_empty"))
	}
	return secret, nil
}

// readTriggerBody reads the request body from a file or stdin ("-"), bounded by the
// largest body any trigger may accept.
func readTriggerBody(path string) ([]byte, error) {
	var r io.Reader
	if path == "-" {
		r = toolStdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(io.LimitReader(r, config.MaxTriggerBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > config.MaxTriggerBodyBytes {
		return nil, fmt.Errorf("%s", cliText("cli.tool.trigger.invoke.body_too_large", "max", config.MaxTriggerBodyBytes))
	}
	return data, nil
}

// triggerStateText is the one-word state of a trigger: ready, disabled, or unavailable with
// the reasons.
func triggerStateText(v trigger.View) string {
	switch {
	case !v.Enabled:
		return cliText("cli.tool.trigger.state_disabled")
	case !v.Available:
		reasons := make([]string, 0, len(v.Issues))
		for _, is := range v.Issues {
			reasons = append(reasons, triggerIssueText(config.TriggerIssue{Code: is.Code, Arg: is.Arg}))
		}
		return cliText("cli.tool.trigger.state_unavailable", "reasons", strings.Join(reasons, "; "))
	}
	return cliText("cli.tool.trigger.state_ready")
}

func triggerLastText(d *trigger.DeliveryView) string {
	if d == nil {
		return "-"
	}
	return fmt.Sprintf("%s %s %s", time.Unix(d.ReceivedAt, 0).Format("2006-01-02 15:04:05"), d.Status, d.EventHash)
}

func triggerDeliveryTail(d trigger.DeliveryView) string {
	out := ""
	if d.JobID != "" {
		out += " job=" + d.JobID
	}
	if d.Error != "" {
		out += " err=" + d.Error
	}
	return out
}
