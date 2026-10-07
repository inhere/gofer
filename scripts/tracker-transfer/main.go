// tracker-transfer is a one-time offline repository tracker transfer tool.
// It never discovers a source or target implicitly and never syncs a server.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/inhere/gofer/internal/tracker"
)

type values []string

func (v *values) String() string { return fmt.Sprint([]string(*v)) }
func (v *values) Set(s string) error {
	*v = append(*v, s)
	return nil
}

func requireNamed(paths ...string) error {
	for _, value := range paths {
		if value == "" {
			return errors.New("explicit --source-tracker, --bundle, and target/selection flags are required; see -h")
		}
	}
	return nil
}

func printJSON(value any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("use inspect, export, or import")
	}
	switch args[0] {
	case "inspect":
		flags := flag.NewFlagSet("inspect", flag.ContinueOnError)
		source := flags.String("source-tracker", "", "exact source .gofer/tracker directory")
		tag := flags.String("tag", "", "candidate tag filter")
		query := flags.String("query", "", "candidate key/title/content filter")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if err := requireNamed(*source); err != nil {
			return err
		}
		candidate, err := tracker.FindTransferCandidates(*source, *tag, *query)
		if err != nil {
			return err
		}
		return printJSON(candidate)
	case "export":
		flags := flag.NewFlagSet("export", flag.ContinueOnError)
		source := flags.String("source-tracker", "", "exact source .gofer/tracker directory")
		bundlePath := flags.String("bundle", "", "output reviewable JSON bundle")
		prefix := flags.String("prefix", "", "new tracker issue prefix, e.g. gofer")
		projectKey := flags.String("project-key", "", "new tracker project key")
		var issues, memories values
		flags.Var(&issues, "issue-id", "selected issue ID (repeat)")
		flags.Var(&memories, "memory-key", "selected memory key (repeat)")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if err := requireNamed(*source, *bundlePath, *prefix, *projectKey); err != nil {
			return err
		}
		bundle, err := tracker.PrepareTransfer(*source, tracker.TransferSelection{IssueIDs: issues, MemoryKeys: memories}, *prefix, *projectKey)
		if err != nil {
			return err
		}
		if err := tracker.WriteTransferBundle(*source, *bundlePath, bundle); err != nil {
			return err
		}
		if err := printJSON(map[string]any{"bundle": *bundlePath, "digest": bundle.Digest, "issues": len(bundle.Issues), "memories": len(bundle.Memories), "boundaries": bundle.Boundaries}); err != nil {
			return err
		}
		if len(bundle.Boundaries) > 0 {
			return fmt.Errorf("%d unresolved issue references; extend --issue-id allowlist before import", len(bundle.Boundaries))
		}
		return nil
	case "import":
		flags := flag.NewFlagSet("import", flag.ContinueOnError)
		source := flags.String("source-tracker", "", "exact source .gofer/tracker directory")
		target := flags.String("target-root", "", "existing target repository root")
		bundlePath := flags.String("bundle", "", "reviewed JSON bundle")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if err := requireNamed(*source, *target, *bundlePath); err != nil {
			return err
		}
		bundle, err := tracker.ReadTransferBundle(*bundlePath)
		if err != nil {
			return err
		}
		published, err := tracker.ImportTransfer(*source, *target, bundle)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"published": published, "tracker_id": bundle.TargetTrackerID, "prefix": bundle.TargetPrefix, "project_key": bundle.ProjectKey, "bundle_digest": bundle.Digest})
	default:
		return fmt.Errorf("unknown action %q; use inspect, export, or import", args[0])
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tracker-transfer:", err)
		os.Exit(1)
	}
}
