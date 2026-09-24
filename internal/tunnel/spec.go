package tunnel

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// ForwardSpec describes a local listener and remote target.
type ForwardSpec struct {
	Network   string
	Bind      string
	LocalPort int
	Target    string
}

// ParseForwardSpec parses [bind:]lport:host:port.
func ParseForwardSpec(s string) (ForwardSpec, error) {
	network := "tcp"
	if strings.HasPrefix(strings.ToLower(s), "udp/") {
		network = "udp"
		s = s[4:]
	}
	if strings.Contains(s, "/") {
		return ForwardSpec{}, fmt.Errorf("invalid spec")
	}
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || idx == len(s)-1 {
		return ForwardSpec{}, fmt.Errorf("invalid spec")
	}
	port, targetPart := s[idx+1:], s[:idx]
	var targetHost, prefix string
	if strings.HasSuffix(targetPart, "]") {
		open := strings.LastIndex(targetPart, "[")
		if open < 0 || open == 0 || open+1 >= len(targetPart) {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		targetHost, prefix = targetPart[open:], targetPart[:open]
		if !strings.HasSuffix(prefix, ":") {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		prefix = strings.TrimSuffix(prefix, ":")
	} else {
		colon := strings.LastIndex(targetPart, ":")
		if colon < 0 {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		targetHost, prefix = targetPart[colon+1:], targetPart[:colon]
	}
	f := ForwardSpec{Bind: "127.0.0.1", Network: network}
	local := prefix
	if strings.HasPrefix(prefix, "[") {
		j := strings.Index(prefix, "]:")
		if j < 0 {
			return ForwardSpec{}, fmt.Errorf("invalid spec")
		}
		f.Bind = strings.Trim(prefix[1:j+1], "[]")
		local = prefix[j+2:]
	} else if j := strings.LastIndex(prefix, ":"); j >= 0 {
		f.Bind = prefix[:j]
		local = prefix[j+1:]
	}
	var err error
	f.LocalPort, err = strconv.Atoi(local)
	if err != nil {
		return ForwardSpec{}, fmt.Errorf("invalid local port %q", local)
	}
	f.Target = targetHost + ":" + port
	if f.LocalPort < 0 || f.LocalPort > 65535 {
		return f, fmt.Errorf("invalid local port")
	}
	if e := ValidateTarget(f.Target); e != nil {
		return f, e
	}
	return f, nil
}

// ListenAddr returns bind:localport.
func (f ForwardSpec) ListenAddr() string { return net.JoinHostPort(f.Bind, strconv.Itoa(f.LocalPort)) }

// SplitSpecs flattens the spec arguments a command was given. Each argument may carry
// several rules separated by commas — the way operators naturally write them
// (`udp/21845:192.168.0.253:21845,1502:192.168.0.205:502`) — and the space-separated
// form stays valid, so `a,b`, `a b` and a mix of the two all mean the same thing.
// Whitespace around a rule is trimmed and empty items are dropped; the order is
// preserved, so `spec #N` in a parse error names the position the operator wrote.
//
// A comma is never part of a rule: an IPv6 target is written `[::1]:port`, so no rule
// can be broken in half by the split.
//
// A list with no usable entry is an error rather than an empty success — "no specs"
// is reported by the caller, not silently accepted here.
func SplitSpecs(args []string) ([]string, error) {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		for _, part := range strings.Split(arg, ",") {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no forward spec given")
	}
	return out, nil
}

// ParseSpecs parses a flattened spec list (see SplitSpecs) and names the entry that
// failed. A comma-joined list of four rules that answers only "invalid spec" leaves
// the operator bisecting the line by hand, so the error carries the position and the
// original text of the offending rule.
func ParseSpecs(specs []string) ([]ForwardSpec, error) {
	out := make([]ForwardSpec, 0, len(specs))
	for i, s := range specs {
		f, err := ParseForwardSpec(s)
		if err != nil {
			return nil, fmt.Errorf("spec #%d %q: %w", i+1, s, err)
		}
		out = append(out, f)
	}
	return out, nil
}
