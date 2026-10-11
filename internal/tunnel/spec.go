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

// NetworkSOCKS is the ForwardSpec.Network of a dynamic forward (`socks/[bind:]port`):
// a local SOCKS5 / HTTP CONNECT listener whose target is chosen per connection. Its
// Target is SOCKSTarget, a display placeholder — every connection still goes through
// the ordinary tunnel auth and the worker's allowlist with its real target.
const (
	NetworkSOCKS = "socks"
	SOCKSTarget  = "*"
)

// IsSOCKS reports whether f is a dynamic (SOCKS) forward.
func (f ForwardSpec) IsSOCKS() bool { return f.Network == NetworkSOCKS }

// ParseForwardSpec parses one forward rule:
//
//	[udp/][bind:]lport:host:port   static forward
//	[udp/]=ip:port                 same address: listen on ip:port, forward to ip:port
//	socks/[bind:]port              dynamic forward (SOCKS5 / HTTP CONNECT), loopback only
func ParseForwardSpec(s string) (ForwardSpec, error) {
	if strings.HasPrefix(strings.ToLower(s), NetworkSOCKS+"/") {
		return parseSOCKSSpec(s[len(NetworkSOCKS)+1:])
	}
	network := "tcp"
	if strings.HasPrefix(strings.ToLower(s), "udp/") {
		network = "udp"
		s = s[4:]
	}
	if strings.HasPrefix(s, "=") {
		return parseSameAddrSpec(network, s[1:])
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

// parseSOCKSSpec reads the `[bind:]port` half of a socks/ rule. The bind must be a
// loopback address: the listener has no authentication, so binding it to a LAN
// address would open a proxy to every target the worker allows.
func parseSOCKSSpec(rest string) (ForwardSpec, error) {
	f := ForwardSpec{Network: NetworkSOCKS, Bind: "127.0.0.1", Target: SOCKSTarget}
	port := rest
	if strings.Contains(rest, ":") {
		h, p, err := net.SplitHostPort(rest)
		if err != nil {
			return ForwardSpec{}, fmt.Errorf("invalid socks spec: want socks/[bind:]port")
		}
		f.Bind, port = h, p
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return ForwardSpec{}, fmt.Errorf("invalid local port %q", port)
	}
	f.LocalPort = n
	if !isLoopbackBind(f.Bind) {
		return ForwardSpec{}, fmt.Errorf("socks listener must bind a loopback address (127.0.0.1 / ::1), got %q: it has no authentication", f.Bind)
	}
	return f, nil
}

func isLoopbackBind(h string) bool {
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// parseSameAddrSpec reads the `ip:port` half of an `=ip:port` rule: listen on the
// target's own address so a local client is configured with the real address. The
// host must be an IP literal — it is also the local bind address.
func parseSameAddrSpec(network, rest string) (ForwardSpec, error) {
	h, p, err := net.SplitHostPort(rest)
	if err != nil {
		return ForwardSpec{}, fmt.Errorf("invalid spec: want =ip:port (IPv6: =[addr]:port)")
	}
	if net.ParseIP(h) == nil {
		return ForwardSpec{}, fmt.Errorf("=%s: host must be an IP address (it is also the local bind address)", rest)
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return ForwardSpec{}, fmt.Errorf("invalid port %q", p)
	}
	f := ForwardSpec{Network: network, Bind: h, LocalPort: n, Target: net.JoinHostPort(h, p)}
	if err := ValidateTarget(f.Target); err != nil {
		return ForwardSpec{}, err
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
