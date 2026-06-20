// Package configtmpl embeds the canonical gofer.example.yaml so that
// `gofer init` can write the same starter template that ships as the documented
// example — one source of truth, no drift between the example file and what init
// emits. The Go source lives next to the YAML (the //go:embed directive cannot
// reference parent directories), keeping config/gofer.example.yaml the single
// authoritative copy.
package configtmpl

import _ "embed"

// ExampleYAML is the embedded contents of config/gofer.example.yaml. `gofer
// init` writes it verbatim; the example-parse test decodes it to guard against
// the template drifting away from the config structs.
//
//go:embed gofer.example.yaml
var ExampleYAML string
