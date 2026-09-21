package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	yaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"

	"github.com/inhere/gofer/internal/util"
)

// managedTopKeys are the top-level keys this tool owns and re-emits on save: every
// exported field of Config, so the set can never drift from the model. Any other
// top-level key found in the source file is preserved as-is.
var managedTopKeys = managedTopKeySet()

// managedTopKeySet derives the managed keys from Config's yaml tags (the struct IS
// the definition of what gofer owns).
func managedTopKeySet() map[string]bool {
	t := reflect.TypeOf(Config{})
	out := make(map[string]bool, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported: never (de)serialized
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		out[name] = true
	}
	return out
}

// Save writes cfg back to path as YAML.
//
// Critical (§12): when the target file already exists, the human's own text must
// survive the rewrite. Two rules, both keyed on TOP-LEVEL keys:
//
//   - A key this tool does NOT manage (e.g. `custom_top: 123`) is carried over from
//     the original file verbatim, text and comments included.
//   - A MANAGED key is re-rendered from the struct — but only if its canonical
//     serialization actually changed (surgical save, bd h-aii-kd57). An untouched
//     block therefore keeps the operator's comments, key order and (AGT-02)
//     `interactive_args: []` exactly as written, while an edit still re-emits the
//     whole block from the struct (comments INSIDE an edited block are lost).
//
// If the file does not exist, parent directories are created and a clean config
// is written.
func Save(path string, cfg *Config) error {
	if path == "" {
		return fmt.Errorf("save config: empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	out, err := render(abs, cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(abs, out, 0o644); err != nil {
		return fmt.Errorf("write config %s: %w", abs, err)
	}
	return nil
}

// render produces the final YAML bytes: the managed config re-rendered, with every
// managed top-level key whose value did NOT change taken from the original file text
// instead, and every unknown top-level key carried over verbatim.
func render(abs string, cfg *Config) ([]byte, error) {
	newBytes, err := yaml.Marshal(withoutInjectedAgents(cfg))
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	orig, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return newBytes, nil
		}
		return nil, fmt.Errorf("read existing config %s: %w", abs, err)
	}

	oldBytes, err := renderOriginal(orig)
	if err != nil {
		// The old file cannot be decoded as a Config (a hand-broken or exotic
		// document): emit the freshly rendered config alone rather than failing the
		// whole save.
		return newBytes, nil
	}
	merged, err := mergeTopKeys(orig, oldBytes, newBytes)
	if err != nil {
		// Same on an AST surprise: the save must still land. Unknown keys are the
		// casualty here, which is why the merge above is total for anything the
		// parser can read.
		return newBytes, nil
	}
	return merged, nil
}

// renderOriginal decodes the file text with the SAME decoder and re-marshals it, so
// each managed key has a canonical "what the file already says" rendering to compare
// the new one against.
//
// It deliberately skips ApplyDefaults: the question is "does this save change what
// the file says?", not "is the in-memory config fully defaulted?". A default that
// only materializes in memory (server.web_enabled, storage subdirs) therefore does
// not count as an edit — if it did, every save would rewrite half the file.
func renderOriginal(orig []byte) ([]byte, error) {
	old := &Config{}
	if err := yaml.Unmarshal(orig, old); err != nil {
		return nil, err
	}
	return yaml.Marshal(withoutInjectedAgents(old))
}

// mergeTopKeys builds the saved document from the ORIGINAL TEXT: every top-level
// block is emitted verbatim — comments, spacing, key order — except a MANAGED block
// whose canonical rendering changed, which is replaced by the freshly rendered node.
// Managed keys the file never had are appended at the end (the struct's order).
//
// Text splicing (rather than re-rendering preserved blocks from the AST) is what makes
// "unchanged" mean byte-identical: goccy's renderer normalizes inline-comment spacing,
// so an AST round-trip would quietly reformat the operator's file even when nothing
// about it changed.
func mergeTopKeys(orig, oldBytes, newBytes []byte) ([]byte, error) {
	blocks, err := topBlocks(orig)
	if err != nil {
		return nil, err
	}
	oldFile, err := parser.ParseBytes(oldBytes, 0)
	if err != nil {
		return nil, err
	}
	newFile, err := parser.ParseBytes(newBytes, 0)
	if err != nil {
		return nil, err
	}
	oldMap, newMap := topMapping(oldFile), topMapping(newFile)
	if oldMap == nil || newMap == nil {
		return nil, fmt.Errorf("unexpected top-level yaml structure")
	}

	lines := make([]string, 0, util.CapSum(len(blocks), len(newMap.Values)))
	kept := make(map[string]bool, len(blocks))
	for _, b := range blocks {
		kept[b.key] = true
		if !managedTopKeys[b.key] {
			lines = append(lines, b.lines...) // not ours: verbatim
			continue
		}
		newNode := mappingValue(newMap, b.key)
		if newNode == nil {
			continue // the key is gone from the config: drop the whole block
		}
		if oldNode := mappingValue(oldMap, b.key); oldNode != nil && oldNode.String() == newNode.String() {
			lines = append(lines, b.lines...) // unchanged: verbatim
			continue
		}
		// Changed: keep the block's head comment (a file header, usually) and replace
		// the key itself.
		lines = append(lines, b.head...)
		lines = append(lines, splitLines(newNode.String())...)
		lines = append(lines, b.tail...)
	}
	for _, v := range newMap.Values {
		key := nodeKey(v)
		if !managedTopKeys[key] || kept[key] {
			continue
		}
		lines = append(lines, splitLines(v.String())...)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// block is one top-level key's raw slice of the file, split so a replacement can keep
// the parts that do not belong to the key's own value:
//
//	# a head comment          <- head
//	key:                      <- lines (from the key line through the last non-blank
//	  child: 1                   line, i.e. body+tail)
//	                          <- tail (the blank lines separating it from the next block)
type block struct {
	key   string
	head  []string
	tail  []string
	lines []string // the original slice, byte-for-byte
}

// topBlocks splits the document into its top-level blocks, in file order. Comments
// above a key (which goccy attaches to that key's node) stay with it — including a
// document header above the first key.
func topBlocks(orig []byte) ([]block, error) {
	f, err := parser.ParseBytes(orig, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	m := topMapping(f)
	if m == nil {
		return nil, fmt.Errorf("unexpected top-level yaml structure")
	}
	lines := strings.Split(string(orig), "\n")

	starts := make([]int, 0, len(m.Values))
	keys := make([]string, 0, len(m.Values))
	for _, v := range m.Values {
		keyLine := v.Key.GetToken().Position.Line
		start := keyLine - 1
		if c := v.GetComment(); c != nil {
			if head := c.GetToken().Position.Line - 1; head >= 0 && head < start {
				start = head
			}
		}
		if start < 0 || keyLine <= 0 || keyLine > len(lines) {
			return nil, fmt.Errorf("unexpected position for top-level key %q", nodeKey(v))
		}
		starts = append(starts, start)
		keys = append(keys, nodeKey(v))
	}

	out := make([]block, 0, len(m.Values))
	for i := range m.Values {
		keyLine := m.Values[i].Key.GetToken().Position.Line - 1
		end := len(lines)
		if i+1 < len(starts) {
			end = starts[i+1]
		}
		// The block body ends at its last non-blank line; the blank lines after it
		// separate it from the next block and are re-emitted after a replacement.
		bodyEnd := keyLine
		for j := keyLine; j < end; j++ {
			if strings.TrimSpace(lines[j]) != "" {
				bodyEnd = j
			}
		}
		out = append(out, block{
			key:   keys[i],
			head:  lines[starts[i]:keyLine],
			tail:  lines[bodyEnd+1 : end],
			lines: lines[starts[i]:end],
		})
	}
	return out, nil
}

// splitLines splits a rendered node into lines for the splice above.
func splitLines(s string) []string { return strings.Split(s, "\n") }

// nodeKey returns a top-level mapping value's key name. It reads the KEY token rather
// than Key.String(): a key with a trailing comment renders as `key # comment`, which
// would never match the managed set. (bd h-aii-kd57 uncovered this on inspection: the
// old unknown-key pass would have treated `agents: # note` as an unknown key.)
func nodeKey(v *ast.MappingValueNode) string {
	if tok := v.Key.GetToken(); tok != nil {
		return tok.Value
	}
	return v.Key.String()
}

// mappingValue returns a top-level mapping's node for key, or nil.
func mappingValue(m *ast.MappingNode, key string) *ast.MappingValueNode {
	for _, v := range m.Values {
		if nodeKey(v) == key {
			return v
		}
	}
	return nil
}

// withoutInjectedAgents returns the config that should actually be serialized: cfg
// itself when nothing was runtime-injected (so a plain config saves byte-identically
// to before), otherwise a SHALLOW COPY whose Agents map drops every key that
// agent.Resolve materialized from a built-in template.
//
// This is the write-back isolation (P2 T0-A). A runtime-materialized template is NOT
// operator configuration: persisting it would (a) grow a config file with agents the
// operator never wrote, and (b) promote it to an explicitly declared agent — which by
// the iron rule is kept forever, even after its CLI is uninstalled. Since `agents` is
// a managed top-level key (it is re-emitted from the struct, not preserved from the
// file text), stripping here is the ONLY place that can keep templates out of the file.
//
// A config whose agents were ALL injected renders its Agents map back to nil, so it
// never gains an `agents:` line the operator never had.
func withoutInjectedAgents(cfg *Config) *Config {
	if cfg == nil || len(cfg.injectedAgents) == 0 {
		return cfg
	}
	clone := *cfg // shallow: only Agents is replaced, every other field is shared
	kept := make(map[string]AgentConfig, len(cfg.Agents))
	for key, ac := range cfg.Agents {
		if cfg.injectedAgents[key] {
			continue
		}
		kept[key] = ac
	}
	if len(kept) == 0 {
		kept = nil
	}
	clone.Agents = kept
	return &clone
}

// topMapping returns the top-level mapping of a parsed document, normalizing a
// single-pair document (*MappingValueNode) into a *MappingNode.
func topMapping(f *ast.File) *ast.MappingNode {
	if f == nil || len(f.Docs) == 0 {
		return nil
	}
	switch b := f.Docs[0].Body.(type) {
	case *ast.MappingNode:
		return b
	case *ast.MappingValueNode:
		return &ast.MappingNode{Values: []*ast.MappingValueNode{b}}
	}
	return nil
}
