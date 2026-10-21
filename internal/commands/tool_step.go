package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gookit/gcli/v3"

	"github.com/inhere/gofer/internal/workflowstep"
)

// tool_step.go implements `gofer tool step <name>` — how a built-in workflow step
// (`uses: gofer/<name>`, design 2026-10-21 §一·5) executes on a runner. It is an
// INTERNAL command: the workflow engine submits it as an internal exec job on the step's
// own runner, with the step's `with:` block as JSON on stdin, and reads the last stdout
// line as the step's result.
//
// Per G021 this file only binds, forwards and prints; the steps themselves live in
// internal/workflowstep and run on top of internal/delivery.

// toolStepTimeout bounds one step execution end-to-end. A wait-ci / wait-assets step
// carries its own timeout in `with`; this is the outer ceiling (a wedged API call must not
// hang the runner forever).
const toolStepTimeout = 60 * time.Minute

var toolStepOpts = struct {
	with string
	dir  string
}{}

// NewToolStepCmd builds the sub-command. It is registered (hidden) under `gofer tool`.
func NewToolStepCmd() *gcli.Command {
	return &gcli.Command{
		Name: "step",
		Desc: "Run a built-in workflow step reading its with: block from stdin (internal)",
		Config: func(c *gcli.Command) {
			c.StrOpt(&toolStepOpts.with, "with", "", "", "with: block as JSON (default: read stdin)")
			c.StrOpt(&toolStepOpts.dir, "dir", "", "", "working directory (default: the current one)")
			c.AddArg("name", "built-in step name (push, wait-ci, wait-assets)", true)
		},
		Func: runToolStep,
	}
}

// runToolStep executes one built-in step. The step's JSON result is the last stdout line;
// a failed step prints {"ok":false,"error":"…"} and exits non-zero, so the workflow job
// fails with the step's own message instead of an opaque exit code.
func runToolStep(c *gcli.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = strings.TrimSpace(args[0])
	}
	if name == "" {
		return toolFail("step name is required (known: %s)", strings.Join(workflowstep.Names(), ", "))
	}
	raw := strings.TrimSpace(toolStepOpts.with)
	if raw == "" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		if err != nil {
			return toolFail("read the with block: %v", err)
		}
		raw = strings.TrimSpace(string(b))
	}
	with := map[string]any{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &with); err != nil {
			return toolFail("read the with block: %v", err)
		}
	}
	dir := toolStepOpts.dir
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return toolFail("resolve the working directory: %v", err)
		}
		dir = wd
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolStepTimeout)
	defer cancel()
	res, err := workflowstep.Run(ctx, name, with, dir)
	if err != nil {
		b, _ := json.Marshal(map[string]any{"ok": false, "step": name, "error": err.Error()})
		c.Println(string(b))
		return toolFail("gofer/%s: %v", name, err)
	}
	b, _ := json.Marshal(map[string]any{"ok": true, "step": name, "result": res})
	c.Println(string(b))
	// The step's result is also written as result.json, so the next workflow step can refer
	// to it as ${steps.<step>.result} (the same file an exec / agent job writes under
	// $GOFER_RESULT_DIR); the printed line stays the job's stdout.
	writeStepResult(res)
	return nil
}

// writeStepResult writes the step's JSON result to $GOFER_RESULT_DIR/result.json when the
// job set that directory (a plain `gofer tool step` run by hand has none). A write failure
// is only logged: the step itself has already run, and its stdout line carries the result.
func writeStepResult(res any) {
	dir := strings.TrimSpace(os.Getenv("GOFER_RESULT_DIR"))
	if dir == "" {
		return
	}
	b, err := json.Marshal(res)
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(dir, "result.json"), b, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write result.json: %v\n", err)
	}
}
