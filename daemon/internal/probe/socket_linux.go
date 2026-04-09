//go:build linux

package probe

import (
	"crypto/tls"
	"net/http"
	"syscall"
)

// controlWithFWMark returns a DialContext control function that sets SO_MARK.
func controlWithFWMark(fwmark uint32) func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var setsockoptErr error
		err := c.Control(func(fd uintptr) {
			setsockoptErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, int(fwmark))
		})
		if err != nil {
			return err
		}
		return setsockoptErr
	}
}

func transportWithSkipVerify(t *http.Transport) *http.Transport {
	t2 := t.Clone()
	t2.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec
	// Preserve the original DialContext (which carries SO_MARK fwmark binding)
	// so HTTPS probes are routed through the correct routing table.
	return t2
}
