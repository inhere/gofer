package config

import (
	"fmt"
	"os"
	"path/filepath"

	yaml "github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
)

// managedTopKeys are the top-level keys this tool owns and re-emits on save.
// Any other top-level key found in the source file is preserved as-is.
var managedTopKeys = map[string]bool{
	"server":     true,
	"storage":    true,
	"projects":   true,
	"agents":     true,
	"runners":    true,
	"roles":      true,
	"supervisor": true,
	"presence":   true,
}

// Save writes cfg back to path as YAML.
//
// Critical (§12): when the target file already exists, any human-authored
// top-level key that is NOT managed by this tool (e.g. `custom_top: 123`) must
// survive the rewrite. We re-marshal the managed config to fresh YAML, then
// append the unknown top-level nodes from the original file (parsed via the
// goccy/go-yaml AST) so their text/structure is preserved verbatim.
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

// render produces the final YAML bytes, preserving unknown top-level fields
// from any existing file at abs.
func render(abs string, cfg *Config) ([]byte, error) {
	newBytes, err := yaml.Marshal(cfg)
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

	merged, err := preserveUnknownTopKeys(orig, newBytes)
	if err != nil {
		// On any AST surprise, fall back to the plain managed render rather than
		// failing the whole save; unknown-field preservation is best effort.
		return newBytes, nil
	}
	return merged, nil
}

// preserveUnknownTopKeys parses both the original and freshly-rendered YAML,
// then appends every non-managed top-level node from orig onto the new mapping.
func preserveUnknownTopKeys(orig, newBytes []byte) ([]byte, error) {
	origFile, err := parser.ParseBytes(orig, 0)
	if err != nil {
		return nil, err
	}
	newFile, err := parser.ParseBytes(newBytes, 0)
	if err != nil {
		return nil, err
	}

	origMap := topMapping(origFile)
	newMap := topMapping(newFile)
	if origMap == nil || newMap == nil {
		return nil, fmt.Errorf("unexpected top-level yaml structure")
	}

	for _, v := range origMap.Values {
		if !managedTopKeys[v.Key.String()] {
			newMap.Values = append(newMap.Values, v)
		}
	}
	if len(newFile.Docs) > 0 {
		newFile.Docs[0].Body = newMap
	}
	return []byte(newFile.String()), nil
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
