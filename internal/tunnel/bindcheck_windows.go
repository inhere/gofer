package tunnel

import (
	"errors"
	"syscall"
)

// wsaEADDRNOTAVAIL is WSAEADDRNOTAVAIL: "The requested address is not valid in its context".
const wsaEADDRNOTAVAIL = syscall.Errno(10049)

func isAddrNotAvail(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == wsaEADDRNOTAVAIL
}
