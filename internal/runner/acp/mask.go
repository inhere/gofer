package acp

import (
	"bytes"
	"encoding/json"
)

// maskEvent returns event with mask applied to every string value, at any depth
// (message / prompt / thought text, tool titles and raw input, structured tool
// content). The event is walked as a decoded value, BEFORE it is JSON-encoded: a
// secret containing a quote or newline would otherwise be escaped in the encoded line
// and slip past a plain substring replacement. A value that cannot be round-tripped is
// returned unmasked-but-safe: replaced by a bare marker rather than written as is.
func maskEvent(event map[string]any, mask func(string) string) map[string]any {
	raw, err := json.Marshal(event)
	if err != nil {
		return event
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep numbers exactly as encoded
	var v any
	if err := dec.Decode(&v); err != nil {
		return map[string]any{"t": event["t"], "masked": "unreadable"}
	}
	out, ok := maskValue(v, mask).(map[string]any)
	if !ok {
		return map[string]any{"t": event["t"], "masked": "unreadable"}
	}
	return out
}

func maskValue(v any, mask func(string) string) any {
	switch x := v.(type) {
	case string:
		return mask(x)
	case []any:
		for i := range x {
			x[i] = maskValue(x[i], mask)
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = maskValue(e, mask)
		}
		return x
	}
	return v
}
