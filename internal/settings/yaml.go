package settings

import (
	"github.com/goccy/go-yaml"
)

// yamlUnmarshal parses an import document. It is a variable so a test can pin the parse
// error path without building an unparsable string for the real decoder.
var yamlUnmarshal = func(data []byte, v any) error { return yaml.Unmarshal(data, v) }
