package brief

import (
	"regexp"
	"sort"
	"strings"
)

// mentions reports whether text names id as a whole token: the characters around it
// are not part of an id, and it is not the prefix of a child id (`gofer-x` does not
// match inside `gofer-x.3`).
func mentions(text, id string) bool {
	if id == "" {
		return false
	}
	for from := 0; ; {
		i := strings.Index(text[from:], id)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(id)
		if (start == 0 || !idByte(text[start-1])) && !continuesID(text, end) {
			return true
		}
		from = start + 1
	}
}

func idByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}

// continuesID reports whether the id ending at text[end] goes on (`x.3`, `x-y`).
func continuesID(text string, end int) bool {
	if end >= len(text) {
		return false
	}
	if idByte(text[end]) {
		return true
	}
	return text[end] == '.' && end+1 < len(text) && text[end+1] >= '0' && text[end+1] <= '9'
}

// shortChild matches the `.N` shorthand of a sibling id written after a full one
// ("gofer-x.1（…）、.2、.5"): a dot and digits after a separator.
var shortChild = regexp.MustCompile(`(?:^|[\s、/,，（(：:])\.(\d+)\b`)

// idScanner finds the known issue ids a text mentions.
type idScanner struct {
	known map[string]bool
	ids   []string // longest first, so a child id is tried before its parent
}

func newIDScanner(ids []string) *idScanner {
	sc := &idScanner{known: make(map[string]bool, len(ids))}
	for _, id := range ids {
		if id != "" && !sc.known[id] {
			sc.known[id] = true
			sc.ids = append(sc.ids, id)
		}
	}
	sort.Slice(sc.ids, func(i, j int) bool {
		if len(sc.ids[i]) != len(sc.ids[j]) {
			return len(sc.ids[i]) > len(sc.ids[j])
		}
		return sc.ids[i] < sc.ids[j]
	})
	return sc
}

// find returns the known ids text mentions, in id order. base, when set, expands the
// `.N` shorthand to base.N for known ids.
func (sc *idScanner) find(text, base string) []string {
	if sc == nil || text == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range sc.ids {
		if !seen[id] && strings.Contains(text, id) && mentions(text, id) {
			seen[id] = true
			out = append(out, id)
		}
	}
	if base != "" {
		for _, m := range shortChild.FindAllStringSubmatch(text, -1) {
			if id := base + "." + m[1]; sc.known[id] && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}
