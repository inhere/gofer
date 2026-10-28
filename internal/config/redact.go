package config

import (
	"net/url"
	"strings"
)

// RedactConfigURL masks credentials and query values before a configured URL is
// shown in diagnostics or a read-only configuration view.
func RedactConfigURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "(configured)"
	}
	if u.User != nil {
		u.User = url.User("***")
	}
	if u.RawQuery != "" {
		q := u.Query()
		for k := range q {
			q[k] = []string{"***"}
		}
		u.RawQuery = q.Encode()
	}
	return strings.NewReplacer("%7B", "{", "%7D", "}").Replace(u.String())
}
