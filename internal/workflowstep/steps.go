// Package workflowstep is the registry of gofer's built-in workflow steps — the steps a
// workflow names with `uses: gofer/<name>` and parametrises with `with:` (design
// 2026-10-21 §一·5). Every entry declares its name, the Go struct its `with` block is
// strictly decoded into (an unknown key is an error, not a silent ignore), its
// validation, where it runs and the worker protocol floor it needs.
//
// It is a SEPARATE package on purpose (G024): `job` must not import `job/workflow`, so
// the registry can not live in workflow. Both sides use it — workflow validates a spec at
// submit, and the `gofer tool step <name>` command executes one on the runner.
package workflowstep

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/inhere/gofer/internal/delivery"
	"github.com/inhere/gofer/internal/wsproto"
)

// UsesPrefix is the only accepted prefix of a `uses:` value. A step is always gofer's
// own code, versioned with the gofer binary; there is no `@version`.
const UsesPrefix = "gofer/"

// Step names the first batch of built-in steps (design §一·5).
const (
	NamePush       = "push"
	NameWaitCI     = "wait-ci"
	NameWaitAssets = "wait-assets"
)

// RunnerExec is where a built-in step executes: an exec job on the step's own runner, the
// same place an ordinary `agent: exec` step runs (which is what makes `runner:` on the
// step meaningful without extra machinery).
const RunnerExec = "exec"

// Step is one registered built-in step.
type Step struct {
	// Name is the part after `gofer/` (push, wait-ci, wait-assets).
	Name string
	// Desc is the one-line description shown by `gofer tool step --help` / docs.
	Desc string
	// Runner is where the step executes (RunnerExec).
	Runner string
	// MinWorkerProtocol is the lowest worker protocol a worker runner must speak: a
	// worker below it has no `gofer tool step` sub-command and would fail the step.
	MinWorkerProtocol int
	// Decode strictly decodes the `with:` block (unknown keys are refused).
	Decode func(with map[string]any) (any, error)
	// Validate checks the decoded arguments; it runs at submit AND before execution.
	Validate func(args any) error
	// Run executes the step in dir and returns the JSON result of the step.
	Run func(ctx context.Context, args any, dir string) (any, error)
}

var registry = []*Step{
	{
		Name: NamePush, Desc: "Push branches and tags and verify the remote shas", Runner: RunnerExec,
		MinWorkerProtocol: wsproto.WorkflowStepMinProtocolVersion,
		Decode:            decodePush,
		Validate:          validatePush,
		Run:               runPush,
	},
	{
		Name: NameWaitCI, Desc: "Wait until the checks of a ref are all finished", Runner: RunnerExec,
		MinWorkerProtocol: wsproto.WorkflowStepMinProtocolVersion,
		Decode:            decodeWaitCI,
		Validate:          validateWaitCI,
		Run:               runWaitCI,
	},
	{
		Name: NameWaitAssets, Desc: "Wait until a release carries every expected asset", Runner: RunnerExec,
		MinWorkerProtocol: wsproto.WorkflowStepMinProtocolVersion,
		Decode:            decodeWaitAssets,
		Validate:          validateWaitAssets,
		Run:               runWaitAssets,
	},
}

// Names lists the registered step names, in registration order.
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, s := range registry {
		out = append(out, s.Name)
	}
	return out
}

// Lookup returns the registered step of an un-prefixed name.
func Lookup(name string) (*Step, bool) {
	for _, s := range registry {
		if s.Name == name {
			return s, true
		}
	}
	return nil, false
}

// ParseUses splits a `uses:` value into the registered name. Only `gofer/<registered>` is
// accepted; anything else is refused with the list of known steps, so a typo in a
// release workflow is a submit error rather than a silent no-op.
func ParseUses(uses string) (string, error) {
	uses = strings.TrimSpace(uses)
	if uses == "" {
		return "", fmt.Errorf("uses: empty (want gofer/<name>)")
	}
	name, ok := strings.CutPrefix(uses, UsesPrefix)
	if !ok {
		return "", fmt.Errorf("uses: %q is not a built-in step (want gofer/<name>, known: %s)", uses, strings.Join(Names(), ", "))
	}
	if _, ok := Lookup(name); !ok {
		return "", fmt.Errorf("uses: unknown built-in step %q (known: %s)", uses, strings.Join(Names(), ", "))
	}
	return name, nil
}

// Validate checks a `uses:` value and its `with:` block at submit time: the step exists
// and the block decodes strictly into the step's own arguments and passes its checks.
func Validate(uses string, with map[string]any) error {
	name, err := ParseUses(uses)
	if err != nil {
		return err
	}
	st, _ := Lookup(name)
	args, err := st.Decode(with)
	if err != nil {
		return fmt.Errorf("%s: %w", uses, err)
	}
	if err := st.Validate(args); err != nil {
		return fmt.Errorf("%s: %w", uses, err)
	}
	return nil
}

// Args decodes a `with:` block into the step's arguments (used before running).
func Args(uses string, with map[string]any) (any, error) {
	name, err := ParseUses(uses)
	if err != nil {
		return nil, err
	}
	st, _ := Lookup(name)
	args, err := st.Decode(with)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", uses, err)
	}
	if err := st.Validate(args); err != nil {
		return nil, fmt.Errorf("%s: %w", uses, err)
	}
	return args, nil
}

// Run executes a built-in step by name in dir (the command's working directory), after
// decoding and validating its `with:` block once more: the executing machine does not
// trust that the submit-time validation and this binary agree.
func Run(ctx context.Context, name string, with map[string]any, dir string) (any, error) {
	st, ok := Lookup(name)
	if !ok {
		return nil, fmt.Errorf("unknown built-in step %q (known: %s)", name, strings.Join(Names(), ", "))
	}
	args, err := st.Decode(with)
	if err != nil {
		return nil, fmt.Errorf("gofer/%s: %w", name, err)
	}
	if err := st.Validate(args); err != nil {
		return nil, fmt.Errorf("gofer/%s: %w", name, err)
	}
	return st.Run(ctx, args, dir)
}

// decodeStrict marshals the untyped `with:` block and decodes it with unknown fields
// refused, so a misspelled argument fails instead of being ignored.
func decodeStrict(with map[string]any, out any) error {
	raw, err := json.Marshal(with)
	if err != nil {
		return fmt.Errorf("with: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("with: %w", err)
	}
	return nil
}

// PushArgs is `gofer/push`'s `with:` block.
type PushArgs struct {
	// Refs are local branch / tag names: a branch is pushed to refs/heads/<ref>, a tag to
	// refs/tags/<ref>.
	Refs []string `json:"refs"`
	// Remote is the git remote to push to ("" = origin).
	Remote string `json:"remote,omitempty"`
	// Proxy overrides git's http.proxy for this push (the project's delivery.proxy).
	Proxy string `json:"proxy,omitempty"`
}

func decodePush(with map[string]any) (any, error) {
	var a PushArgs
	if err := decodeStrict(with, &a); err != nil {
		return nil, err
	}
	return a, nil
}

func validatePush(raw any) error {
	a := raw.(PushArgs)
	if len(a.Refs) == 0 {
		return fmt.Errorf("refs is required (the branches / tags to push)")
	}
	for _, ref := range a.Refs {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("refs contains an empty name")
		}
		if strings.HasPrefix(ref, "-") {
			return fmt.Errorf("ref %q may not start with '-'", ref)
		}
	}
	return nil
}

func runPush(ctx context.Context, raw any, dir string) (any, error) {
	a := raw.(PushArgs)
	refs, err := delivery.PushRefs(ctx, dir, a.Remote, a.Refs, a.Proxy)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "pushed": refs}, nil
}

// WaitCIArgs is `gofer/wait-ci`'s `with:` block.
type WaitCIArgs struct {
	Ref          string   `json:"ref"`
	Required     []string `json:"required,omitempty"`
	CodeScanning bool     `json:"code_scanning,omitempty"`
	TimeoutMin   int      `json:"timeout_min,omitempty"`
	IntervalSec  int      `json:"interval_sec,omitempty"`
	Remote       string   `json:"remote,omitempty"`
}

func decodeWaitCI(with map[string]any) (any, error) {
	var a WaitCIArgs
	if err := decodeStrict(with, &a); err != nil {
		return nil, err
	}
	return a, nil
}

func validateWaitCI(raw any) error {
	a := raw.(WaitCIArgs)
	if strings.TrimSpace(a.Ref) == "" {
		return fmt.Errorf("ref is required (the branch, tag or commit whose checks to wait for)")
	}
	if a.TimeoutMin < 0 || a.IntervalSec < 0 {
		return fmt.Errorf("timeout_min and interval_sec may not be negative")
	}
	return nil
}

func runWaitCI(ctx context.Context, raw any, dir string) (any, error) {
	a := raw.(WaitCIArgs)
	res, err := delivery.WaitCI(ctx, dir, delivery.WaitCIOptions{
		Ref: a.Ref, Required: a.Required, CodeScanning: a.CodeScanning,
		TimeoutMin: a.TimeoutMin, IntervalSec: a.IntervalSec, Remote: a.Remote,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "ci": res}, nil
}

// WaitAssetsArgs is `gofer/wait-assets`'s `with:` block.
type WaitAssetsArgs struct {
	Tag         string   `json:"tag"`
	Assets      []string `json:"assets"`
	TimeoutMin  int      `json:"timeout_min,omitempty"`
	IntervalSec int      `json:"interval_sec,omitempty"`
	Remote      string   `json:"remote,omitempty"`
}

func decodeWaitAssets(with map[string]any) (any, error) {
	var a WaitAssetsArgs
	if err := decodeStrict(with, &a); err != nil {
		return nil, err
	}
	return a, nil
}

func validateWaitAssets(raw any) error {
	a := raw.(WaitAssetsArgs)
	if strings.TrimSpace(a.Tag) == "" {
		return fmt.Errorf("tag is required (the release tag to wait for)")
	}
	if len(a.Assets) == 0 {
		return fmt.Errorf("assets is required (globs of the expected asset names)")
	}
	if a.TimeoutMin < 0 || a.IntervalSec < 0 {
		return fmt.Errorf("timeout_min and interval_sec may not be negative")
	}
	return nil
}

func runWaitAssets(ctx context.Context, raw any, dir string) (any, error) {
	a := raw.(WaitAssetsArgs)
	res, err := delivery.WaitAssets(ctx, dir, delivery.WaitAssetsOptions{
		Tag: a.Tag, Assets: a.Assets, TimeoutMin: a.TimeoutMin, IntervalSec: a.IntervalSec, Remote: a.Remote,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "release": res}, nil
}
