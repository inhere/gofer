package tunnel

import (
	"errors"
	"net/http"
)

// ErrWorkerOffline indicates the selected worker is not connected.
var ErrWorkerOffline = errors.New("worker offline")

// ErrUnsupported indicates a worker protocol older than v5 lacks tunnel support.
var ErrUnsupported = errors.New("tunnel unsupported")

const (
	ConnectPath       = "/v1/tunnels/connect"
	WorkerConnectPath = "/v1/workers/tunnel-connect"
)

// Hello is the worker rendezvous response.
type Hello struct {
	TunnelID   string `json:"tunnel_id"`
	RelayNonce string `json:"relay_nonce"`
	ErrorCode  string `json:"error_code,omitempty"`
	Error      string `json:"error,omitempty"`
}

const (
	CodeDisabled   = "disabled"
	CodeNotAllowed = "not_allowed"
	CodeLimit      = "limit"
	CodeDialFailed = "dial_failed"
	CodeBadTarget  = "bad_target"
)

// HTTPStatusForCode maps worker errors to HTTP status.
func HTTPStatusForCode(c string) int {
	switch c {
	case CodeDisabled, CodeNotAllowed:
		return http.StatusForbidden
	case CodeLimit:
		return http.StatusTooManyRequests
	case CodeDialFailed:
		return http.StatusBadGateway
	case CodeBadTarget:
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}
