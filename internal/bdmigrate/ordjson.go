package bdmigrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// A tiny order-preserving JSON tree. encoding/json maps lose key order, which
// would reshuffle a hand-maintained .claude/settings.json on every migration;
// here objects keep their keys in source order and numbers keep their text, so
// an untouched document re-serializes byte for byte.

type ordObj struct {
	keys []string
	vals map[string]any
}

func newOrdObj() *ordObj { return &ordObj{vals: map[string]any{}} }

func (o *ordObj) get(key string) (any, bool) { v, ok := o.vals[key]; return v, ok }

func (o *ordObj) set(key string, value any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = value
}

func (o *ordObj) del(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// parseOrdered decodes one JSON document.
func parseOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("trailing data after JSON document")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // string, json.Number, bool, nil
	}
	switch delim {
	case '{':
		obj := newOrdObj()
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyTok.(string)
			val, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			obj.set(key, val)
		}
		if _, err := dec.Token(); err != nil { // closing }
			return nil, err
		}
		return obj, nil
	case '[':
		list := []any{}
		for dec.More() {
			val, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, val)
		}
		if _, err := dec.Token(); err != nil { // closing ]
			return nil, err
		}
		return list, nil
	}
	return nil, fmt.Errorf("unexpected delimiter %v", delim)
}

// marshalOrdered renders v with the given indent unit, matching the layout of
// encoding/json's MarshalIndent (empty containers stay compact, no HTML escaping).
func marshalOrdered(v any, indent string) ([]byte, error) {
	var out bytes.Buffer
	if err := writeValue(&out, v, indent, 0); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeValue(out *bytes.Buffer, v any, indent string, depth int) error {
	pad := func(n int) { out.WriteString("\n"); out.WriteString(strings.Repeat(indent, n)) }
	switch t := v.(type) {
	case *ordObj:
		if len(t.keys) == 0 {
			out.WriteString("{}")
			return nil
		}
		out.WriteString("{")
		for i, key := range t.keys {
			if i > 0 {
				out.WriteString(",")
			}
			pad(depth + 1)
			if err := writeScalar(out, key); err != nil {
				return err
			}
			out.WriteString(": ")
			if err := writeValue(out, t.vals[key], indent, depth+1); err != nil {
				return err
			}
		}
		pad(depth)
		out.WriteString("}")
	case []any:
		if len(t) == 0 {
			out.WriteString("[]")
			return nil
		}
		out.WriteString("[")
		for i, item := range t {
			if i > 0 {
				out.WriteString(",")
			}
			pad(depth + 1)
			if err := writeValue(out, item, indent, depth+1); err != nil {
				return err
			}
		}
		pad(depth)
		out.WriteString("]")
	default:
		return writeScalar(out, v)
	}
	return nil
}

func writeScalar(out *bytes.Buffer, v any) error {
	if n, ok := v.(json.Number); ok {
		out.WriteString(n.String())
		return nil
	}
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	out.Truncate(out.Len() - 1) // Encode appends a newline
	return nil
}

// detectIndent returns the document's indent unit (default two spaces).
func detectIndent(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return "  "
}
