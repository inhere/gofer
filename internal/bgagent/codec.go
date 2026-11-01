package bgagent

import (
	"encoding/json"

	"github.com/inhere/gofer/internal/runner"
)

func marshalDetail(m map[string]any) (string, error) {
	b, err := json.Marshal(m)
	return string(b), err
}

func decodeUsage(raw string) *runner.Usage {
	if raw == "" {
		return nil
	}
	var u runner.Usage
	if json.Unmarshal([]byte(raw), &u) != nil {
		return nil
	}
	return &u
}
