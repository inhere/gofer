package tunnel

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

// The dynamic forward (socks/ rules) speaks two proxy handshakes on one port, told
// apart by the first byte: SOCKS5 (0x05) for browsers and SOCKS-aware tools, and HTTP
// CONNECT for tools that only take an HTTP proxy (HTTPS_PROXY=http://...). Only the
// CONNECT command exists in either: no SOCKS BIND / UDP ASSOCIATE, no plain-HTTP
// (absolute-form) proxying, no authentication — the listener is loopback-only.

// Proxy protocols a handshake was read in.
const (
	ProxySOCKS5 = "socks5"
	ProxyHTTP   = "http"
)

// SOCKS5 reply codes (RFC 1928 §6).
const (
	socksSucceeded          byte = 0x00
	socksGeneralFailure     byte = 0x01
	socksNotAllowed         byte = 0x02
	socksHostUnreachable    byte = 0x04
	socksConnRefused        byte = 0x05
	socksTTLExpired         byte = 0x06
	socksCmdNotSupported    byte = 0x07
	socksAddrTypeNotSupport byte = 0x08
)

// ProxyRequest is one accepted handshake: the protocol it arrived in and the
// host:port the client asked for.
type ProxyRequest struct {
	Proto  string
	Target string
}

// errHandshakeReplied marks a handshake the reader already answered with a refusal
// (unsupported method / command / address type, 405): nothing more is written.
var errHandshakeReplied = errors.New("proxy handshake refused")

// ReadProxyRequest reads one SOCKS5 or HTTP CONNECT handshake from br, answering the
// parts of the protocol that precede the target dial (SOCKS method selection) and any
// refusal. On success the caller dials the target and then answers with
// WriteProxyReply. br must be the reader the rest of the connection is read from:
// bytes a client pipelined after the handshake stay buffered in it.
func ReadProxyRequest(br *bufio.Reader, w io.Writer) (ProxyRequest, error) {
	first, err := br.Peek(1)
	if err != nil {
		return ProxyRequest{}, err
	}
	switch b := first[0]; {
	case b == 0x05:
		return readSOCKS5(br, w)
	case b >= 'A' && b <= 'Z':
		return readHTTPConnect(br, w)
	default:
		return ProxyRequest{}, fmt.Errorf("unknown proxy protocol (first byte 0x%02x)", b)
	}
}

func readSOCKS5(br *bufio.Reader, w io.Writer) (ProxyRequest, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return ProxyRequest{}, err
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(br, methods); err != nil {
		return ProxyRequest{}, err
	}
	noAuth := false
	for _, m := range methods {
		if m == 0x00 {
			noAuth = true
		}
	}
	if !noAuth {
		_, _ = w.Write([]byte{0x05, 0xFF})
		return ProxyRequest{}, fmt.Errorf("%w: client offers no supported auth method (only no-auth)", errHandshakeReplied)
	}
	if _, err := w.Write([]byte{0x05, 0x00}); err != nil {
		return ProxyRequest{}, err
	}
	var req [4]byte // VER CMD RSV ATYP
	if _, err := io.ReadFull(br, req[:]); err != nil {
		return ProxyRequest{}, err
	}
	if req[0] != 0x05 {
		return ProxyRequest{}, fmt.Errorf("bad SOCKS version %d in request", req[0])
	}
	var host string
	switch req[3] {
	case 0x01, 0x04: // IPv4 / IPv6
		n := 4
		if req[3] == 0x04 {
			n = 16
		}
		ip := make([]byte, n)
		if _, err := io.ReadFull(br, ip); err != nil {
			return ProxyRequest{}, err
		}
		host = net.IP(ip).String()
	case 0x03: // domain name
		l, err := br.ReadByte()
		if err != nil {
			return ProxyRequest{}, err
		}
		name := make([]byte, int(l))
		if _, err := io.ReadFull(br, name); err != nil {
			return ProxyRequest{}, err
		}
		host = string(name)
	default:
		_ = WriteSOCKSReply(w, socksAddrTypeNotSupport)
		return ProxyRequest{}, fmt.Errorf("%w: address type 0x%02x not supported", errHandshakeReplied, req[3])
	}
	var pb [2]byte
	if _, err := io.ReadFull(br, pb[:]); err != nil {
		return ProxyRequest{}, err
	}
	if req[1] != 0x01 {
		_ = WriteSOCKSReply(w, socksCmdNotSupported)
		return ProxyRequest{}, fmt.Errorf("%w: SOCKS command 0x%02x not supported (only CONNECT)", errHandshakeReplied, req[1])
	}
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(pb[:]))))
	if err := ValidateTarget(target); err != nil {
		_ = WriteSOCKSReply(w, socksGeneralFailure)
		return ProxyRequest{}, fmt.Errorf("%w: %v", errHandshakeReplied, err)
	}
	return ProxyRequest{Proto: ProxySOCKS5, Target: target}, nil
}

func readHTTPConnect(br *bufio.Reader, w io.Writer) (ProxyRequest, error) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return ProxyRequest{}, err
	}
	if req.Body != nil {
		_ = req.Body.Close()
	}
	if req.Method != http.MethodConnect {
		writeHTTPStatus(w, http.StatusMethodNotAllowed, "this gofer tunnel proxy supports SOCKS5 and HTTP CONNECT only")
		return ProxyRequest{}, fmt.Errorf("%w: HTTP method %s not supported (only CONNECT)", errHandshakeReplied, req.Method)
	}
	target := req.Host
	if target == "" {
		target = req.RequestURI
	}
	if err := ValidateTarget(target); err != nil {
		writeHTTPStatus(w, http.StatusBadRequest, err.Error())
		return ProxyRequest{}, fmt.Errorf("%w: %v", errHandshakeReplied, err)
	}
	return ProxyRequest{Proto: ProxyHTTP, Target: target}, nil
}

// WriteSOCKSReply writes a SOCKS5 reply with an all-zero bound address.
func WriteSOCKSReply(w io.Writer, rep byte) error {
	_, err := w.Write([]byte{0x05, rep, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

func writeHTTPStatus(w io.Writer, code int, msg string) {
	_, _ = fmt.Fprintf(w, "HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nConnection: close\r\nContent-Length: %d\r\n\r\n%s\n",
		code, http.StatusText(code), len(msg)+1, msg)
}

// WriteProxyReply answers the handshake once the target dial finished: success, or
// the failure mapped by ProxyFailure.
func WriteProxyReply(w io.Writer, proto string, dialErr error) error {
	if proto == ProxyHTTP {
		if dialErr == nil {
			_, err := io.WriteString(w, "HTTP/1.1 200 Connection established\r\n\r\n")
			return err
		}
		_, code := ProxyFailure(dialErr)
		writeHTTPStatus(w, code, dialErr.Error())
		return nil
	}
	if dialErr == nil {
		return WriteSOCKSReply(w, socksSucceeded)
	}
	rep, _ := ProxyFailure(dialErr)
	return WriteSOCKSReply(w, rep)
}

// ProxyFailure maps a tunnel dial error onto the SOCKS5 reply code and the HTTP
// status the proxy client sees. The connect endpoint's HTTP status is read through
// a TunnelStatus() method (client.TunnelError); a worker dial failure (502) is split
// into "refused" and "unreachable" by its message.
func ProxyFailure(err error) (byte, int) {
	var se interface{ TunnelStatus() int }
	if !errors.As(err, &se) {
		return socksGeneralFailure, http.StatusBadGateway
	}
	switch se.TunnelStatus() {
	case http.StatusForbidden, http.StatusUnauthorized:
		return socksNotAllowed, http.StatusForbidden
	case http.StatusTooManyRequests:
		return socksGeneralFailure, http.StatusServiceUnavailable
	case http.StatusBadGateway:
		if strings.Contains(strings.ToLower(err.Error()), "refused") {
			return socksConnRefused, http.StatusBadGateway
		}
		return socksHostUnreachable, http.StatusBadGateway
	case http.StatusGatewayTimeout:
		return socksTTLExpired, http.StatusGatewayTimeout
	default:
		return socksGeneralFailure, http.StatusBadGateway
	}
}

// bufferedConn is a net.Conn whose reads drain a handshake reader first, so bytes a
// client pipelined right after its handshake are not lost.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
