package tunnel

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ValidateTarget validates host:port targets.
func ValidateTarget(t string) error {
	h, p, e := net.SplitHostPort(t)
	if e != nil {
		return fmt.Errorf("invalid target %q: %v", t, e)
	}
	if h == "" {
		return fmt.Errorf("invalid target %q: host is empty", t)
	}
	if strings.Contains(h, ":") && !strings.HasPrefix(t, "[") {
		return fmt.Errorf("invalid target %q: IPv6 host must use brackets", t)
	}
	if p == "" {
		return fmt.Errorf("invalid target %q: port is invalid", t)
	}
	x, e := strconv.Atoi(p)
	if e != nil || x < 1 || x > 65535 {
		return fmt.Errorf("invalid target %q: port %q is invalid", t, p)
	}
	return nil
}

type rule struct {
	host string
	port string
	net  *net.IPNet
	cidr bool
}

// Allowlist contains parsed outbound target rules.
type Allowlist struct{ rules []rule }

// ParseAllowlist parses host, CIDR and wildcard-port rules.
func ParseAllowlist(es []string) (*Allowlist, error) {
	a := &Allowlist{}
	for _, s := range es {
		h, p, e := net.SplitHostPort(strings.TrimSpace(s))
		if e != nil || h == "" || (p != "*" && (!isPort(p))) {
			return nil, fmt.Errorf("invalid allowlist entry %q", s)
		}
		r := rule{host: strings.ToLower(strings.Trim(h, "[]")), port: p}
		if ip, n, er := net.ParseCIDR(r.host); er == nil {
			n.IP = ip
			r.net = n
			r.cidr = true
		}
		a.rules = append(a.rules, r)
	}
	return a, nil
}
func isPort(p string) bool {
	n, e := strconv.Atoi(p)
	return e == nil && n > 0 && n <= 65535
}

// Empty reports whether no rules are configured.
func (a *Allowlist) Empty() bool { return a == nil || len(a.rules) == 0 }

// Allows reports whether target matches a rule.
func (a *Allowlist) Allows(t string) bool {
	if a == nil {
		return false
	}
	h, p, e := net.SplitHostPort(t)
	if e != nil {
		return false
	}
	n, e := strconv.Atoi(p)
	if e != nil {
		return false
	}
	for _, r := range a.rules {
		if r.port != "*" {
			rn, _ := strconv.Atoi(r.port)
			if rn != n {
				continue
			}
		}
		if r.cidr {
			if ip := net.ParseIP(h); ip != nil && r.net.Contains(ip) {
				return true
			}
		} else if r.host == "*" || strings.EqualFold(strings.Trim(h, "[]"), r.host) {
			return true
		}
	}
	return false
}
