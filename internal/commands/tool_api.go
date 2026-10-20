package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/gookit/gcli/v3"
	"github.com/gookit/goutil/maputil"

	"github.com/inhere/gofer/internal/config"
)

// tool_api.go implements `gofer tool api`: one raw request against the server's HTTP
// API, with the same address and token every other command uses and no decoding in
// between. It exists so an operator (or an agent) can look at what the API answers
// without curl, hand-building a base URL, or hunting for the token.
//
// G033: it is a small utility, so it lives under `gofer tool`, never top level.

// toolApiStderr is where a non-2xx response body goes. A var, not os.Stderr directly,
// so tests can capture it.
var toolApiStderr io.Writer = os.Stderr

var toolApiOpts = struct {
	method     string
	fields     gcli.Strings
	files      gcli.Strings
	input      string
	query      string
	showHeader bool
}{}

// NewToolApiCmd builds the `tool api` subcommand (registered by NewToolCmd).
func NewToolApiCmd() *gcli.Command {
	return &gcli.Command{
		Name:    "api",
		Desc:    "Call the server's HTTP API and print the response (address and token included)",
		Aliases: []string{"curl"},
		Help: `The path is relative to the server's /v1 API, so "jobs" and "/v1/jobs" are the
same request. The address and token come from the same place as everywhere else in the
CLI (-c / --server / --token, or $GOFER_CONFIG_DIR/.env), so nothing has to be typed
twice.

Fields build the body (` + "`application/json`" + `); on a GET they become query parameters
instead. A value is read as JSON when it parses as one (10 → number, true → bool) and as
a plain string otherwise:

  gofer tool api jobs -f status=running -f limit=5
  gofer tool api jobs -X POST --input job.json
  gofer tool api settings/work.draft.min_prompts -X PUT -f value=3
  gofer tool api prompt -F value=@prompt.md          # file content as a string
  gofer tool api jobs -q 'jobs.*.id'

--query picks one value out of the JSON response by path (top.sub, jobs.0.id,
jobs.*.id): a string prints as it is, anything else as indented JSON. A non-2xx answer
exits non-zero and prints the body to stderr; --include also shows the status line and
the response headers.`,
		Config: func(c *gcli.Command) {
			bindConfigFlag(c)
			bindServerFlags(c)
			c.StrOpt(&toolApiOpts.method, "request", "X", "", "HTTP method (default: GET, or POST when a body is given)")
			c.VarOpt(&toolApiOpts.fields, "field", "f", "field as key=value (repeatable; query parameters on a GET)")
			c.VarOpt(&toolApiOpts.files, "file-field", "F", "field as key=@file, the file's content as the string value (repeatable)")
			c.StrOpt(&toolApiOpts.input, "input", "", "", "raw request body from this file ('-' reads stdin)")
			c.StrOpt(&toolApiOpts.query, "query", "q", "", "print this value of the JSON response instead of the whole body")
			c.BoolOpt(&toolApiOpts.showHeader, "include", "i", false, "print the status line and response headers")
			c.AddArg("path", "API path, e.g. jobs or /v1/jobs/abc", true)
		},
		Func: runToolApi,
	}
}

// runToolApi runs `gofer tool api`.
func runToolApi(c *gcli.Command, args []string) error {
	path := normalizeAPIPath(toolArg(c, args, 0, "path"))
	if path == "" {
		return toolFail("usage: gofer tool api [-X METHOD] <path> [-f key=value]...")
	}
	fields, err := toolApiFields()
	if err != nil {
		return toolFail("%v", err)
	}
	body, hasBody, err := toolApiBody()
	if err != nil {
		return toolFail("%v", err)
	}
	method := strings.ToUpper(strings.TrimSpace(toolApiOpts.method))
	if method == "" {
		// Fields on a GET are query parameters, so only a hand-written body makes the
		// call a POST on its own (the help lists the -X spellings for a write).
		method = http.MethodGet
		if hasBody {
			method = http.MethodPost
		}
	}
	contentType := ""
	if hasBody {
		contentType = "application/json"
	}
	switch {
	case method == http.MethodGet && len(fields) > 0:
		// A GET has no body: the fields are the query.
		if strings.Contains(path, "?") {
			path += "&" + encodeAPIFields(fields)
		} else {
			path += "?" + encodeAPIFields(fields)
		}
		body, hasBody = nil, false
		contentType = ""
	case !hasBody && len(fields) > 0:
		raw, err := json.Marshal(fields)
		if err != nil {
			return toolFail("encode body: %v", err)
		}
		body, hasBody, contentType = raw, true, "application/json"
	}

	cli, err := newClient(config.InputCfgFile, jobConnOpts.server, jobConnOpts.token)
	if err != nil {
		return toolFail("%v", err)
	}
	resp, err := cli.DoRaw(method, path, body, contentType)
	if err != nil {
		return toolFail("%v", err)
	}
	if toolApiOpts.showHeader {
		c.Printf("HTTP %d %s\n", resp.Status, http.StatusText(resp.Status))
		keys := make([]string, 0, len(resp.Header))
		for k := range resp.Header {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			c.Printf("%s: %s\n", k, strings.Join(resp.Header[k], ", "))
		}
		c.Println("")
	}
	if resp.Status < 200 || resp.Status >= 300 {
		// The server's own error body is the diagnosis; print it verbatim on stderr and
		// exit non-zero, so a script can branch on the status.
		_, _ = fmt.Fprintln(toolApiStderr, strings.TrimRight(string(resp.Body), "\n"))
		return errorx400(resp.Status)
	}
	if q := strings.TrimSpace(toolApiOpts.query); q != "" {
		return printAPIQuery(c, q, resp.Body)
	}
	printAPIBody(c, resp.Body)
	return nil
}

// errorx400 turns a non-2xx answer into a coded error naming the status.
func errorx400(status int) error {
	return toolFail("server answered HTTP %d %s", status, http.StatusText(status))
}

// normalizeAPIPath accepts both spellings of an API path: `jobs` and `/v1/jobs` are the
// same request, and a query string rides along untouched.
func normalizeAPIPath(raw string) string {
	p := strings.TrimSpace(raw)
	if p == "" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if p != "/v1" && !strings.HasPrefix(p, "/v1/") {
		p = "/v1" + p
	}
	return p
}

// toolApiFields merges -f and -F into one map. A key given twice keeps the last value;
// -F and -f are two ways to spell one field, not two fields.
func toolApiFields() (map[string]any, error) {
	out := make(map[string]any, len(toolApiOpts.fields)+len(toolApiOpts.files))
	for _, f := range toolApiOpts.fields {
		key, value, ok := strings.Cut(f, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("-f %q: field must be key=value", f)
		}
		out[strings.TrimSpace(key)] = apiFieldValue(value)
	}
	for _, f := range toolApiOpts.files {
		key, value, ok := strings.Cut(f, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("-F %q: field must be key=@file", f)
		}
		path := strings.TrimSpace(value)
		if !strings.HasPrefix(path, "@") {
			return nil, fmt.Errorf("-F %q: the value must be @file (use -f for a literal value)", f)
		}
		data, err := readSettingsInput(strings.TrimPrefix(path, "@"))
		if err != nil {
			return nil, fmt.Errorf("-F %s: %v", key, err)
		}
		out[strings.TrimSpace(key)] = string(data)
	}
	return out, nil
}

// apiFieldValue reads a field value as JSON when it is one (a number, a bool, a quoted
// string) and keeps it as text otherwise: `limit=5` must reach the server as a number.
func apiFieldValue(raw string) any {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err == nil {
		switch v.(type) {
		case float64, bool, nil, string:
			return v
		}
	}
	return raw
}

// encodeAPIFields renders fields as a query string (GET spellings).
func encodeAPIFields(fields map[string]any) string {
	q := url.Values{}
	for k, v := range fields {
		q.Set(k, fmt.Sprintf("%v", v))
	}
	return q.Encode()
}

// toolApiBody reads --input (`-` for stdin). An explicit --input wins over -f/-F: the
// caller who wrote the body by hand said exactly what to send.
func toolApiBody() ([]byte, bool, error) {
	path := strings.TrimSpace(toolApiOpts.input)
	if path == "" {
		return nil, false, nil
	}
	data, err := readSettingsInput(path)
	if err != nil {
		return nil, false, fmt.Errorf("read --input %s: %v", path, err)
	}
	return data, true, nil
}

// printAPIBody prints the response: JSON re-indented (it is a terminal, not a stream),
// anything else verbatim (YAML export, logs, text).
func printAPIBody(c *gcli.Command, body []byte) {
	if len(body) == 0 {
		return
	}
	var buf any
	if json.Unmarshal(body, &buf) == nil {
		if enc, err := json.MarshalIndent(buf, "", "  "); err == nil {
			c.Println(string(enc))
			return
		}
	}
	c.Print(string(body))
}

// printAPIQuery prints one value of the response by path (goutil maputil path syntax:
// top.sub, jobs.0.id, jobs.*.id). A path the response does not have is an error, so a
// script can tell "no value" from an empty one.
func printAPIQuery(c *gcli.Command, path string, body []byte) error {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return toolFail("--query needs a JSON response: %v", err)
	}
	val, ok := maputil.GetFromAny(path, data)
	if !ok {
		return toolFail("--query %s: no such value in the response", path)
	}
	switch v := val.(type) {
	case string:
		c.Println(v)
	case nil:
		c.Println("")
	default:
		if enc, err := json.MarshalIndent(v, "", "  "); err == nil {
			c.Println(string(enc))
			return nil
		}
		c.Printf("%v\n", v)
	}
	return nil
}
