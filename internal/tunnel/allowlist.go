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

// portMatch matches the port half of an allowlist entry: any port, or a set of
// inclusive ranges (a single port is kept as a one-wide range).
type portMatch struct {
	any    bool
	ranges [][2]int
}

func (m portMatch) contains(n int) bool {
	if m.any {
		return true
	}
	for _, r := range m.ranges {
		if n >= r[0] && n <= r[1] {
			return true
		}
	}
	return false
}

// parsePorts parses the port half of an entry: "*", a single port, a comma
// separated list, or inclusive ranges ("11740-11743"). A device that speaks on
// several ports is then one line instead of one line per port. "*" may not hide
// inside a list, so an entry's breadth stays readable at a glance.
func parsePorts(p string) (portMatch, error) {
	if strings.TrimSpace(p) == "*" {
		return portMatch{any: true}, nil
	}
	var m portMatch
	for _, part := range strings.Split(p, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return portMatch{}, fmt.Errorf("empty port in %q", p)
		}
		if part == "*" {
			return portMatch{}, fmt.Errorf(`"*" cannot be combined with other ports`)
		}
		lo, hi, isRange := strings.Cut(part, "-")
		if !isRange {
			n, ok := parsePort(part)
			if !ok {
				return portMatch{}, fmt.Errorf("invalid port %q", part)
			}
			m.ranges = append(m.ranges, [2]int{n, n})
			continue
		}
		a, aok := parsePort(strings.TrimSpace(lo))
		b, bok := parsePort(strings.TrimSpace(hi))
		if !aok || !bok {
			return portMatch{}, fmt.Errorf("invalid port range %q", part)
		}
		if a > b {
			return portMatch{}, fmt.Errorf("invalid port range %q: %d is above %d", part, a, b)
		}
		m.ranges = append(m.ranges, [2]int{a, b})
	}
	return m, nil
}

func parsePort(p string) (int, bool) {
	n, e := strconv.Atoi(p)
	return n, e == nil && n > 0 && n <= 65535
}

type rule struct {
	network string
	host    string
	ports   portMatch
	net     *net.IPNet
	cidr    bool
}

// Allowlist contains parsed outbound target rules.
type Allowlist struct{ rules []rule }

// ParseAllowlist parses host, CIDR and port rules. The port half is a single
// port, "*", a comma separated list, or inclusive ranges.
func ParseAllowlist(es []string) (*Allowlist, error) {
	a := &Allowlist{}
	for _, s := range es {
		raw := strings.TrimSpace(s)
		network := "tcp"
		// An entry may name its network: "udp/host:port" (and "tcp/..." for symmetry).
		// Only these two prefixes are stripped; anything else keeps its slash and is
		// caught below, so a typo like "sctp/1.2.3.4:1" fails loudly instead of
		// silently becoming a rule for the host named "sctp/1.2.3.4".
		if i := strings.IndexByte(raw, '/'); i > 0 {
			switch p := strings.ToLower(raw[:i]); p {
			case "tcp", "udp":
				network, raw = p, raw[i+1:]
			}
		}
		h, p, e := net.SplitHostPort(raw)
		if e != nil || h == "" {
			return nil, fmt.Errorf("invalid allowlist entry %q", s)
		}
		ports, pe := parsePorts(p)
		if pe != nil {
			return nil, fmt.Errorf("invalid allowlist entry %q: %v", s, pe)
		}
		r := rule{network: network, host: strings.ToLower(strings.Trim(h, "[]")), ports: ports}
		if ip, n, er := net.ParseCIDR(r.host); er == nil {
			n.IP = ip
			r.net = n
			r.cidr = true
		} else if strings.Contains(r.host, "/") {
			// A slash survives only in a CIDR; anything else is a malformed entry
			// (unknown network prefix, bad mask) and must not become a hostname.
			return nil, fmt.Errorf("invalid allowlist entry %q", s)
		}
		a.rules = append(a.rules, r)
	}
	return a, nil
}

// Empty reports whether no rules are configured.
func (a *Allowlist) Empty() bool { return a == nil || len(a.rules) == 0 }

// Allows reports whether target matches a rule.
func (a *Allowlist) Allows(t string) bool {
	return a.AllowsNetwork("tcp", t)
}
func (a *Allowlist) AllowsNetwork(network, t string) bool {
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
		if r.network != network {
			continue
		}
		if !r.ports.contains(n) {
			continue
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
