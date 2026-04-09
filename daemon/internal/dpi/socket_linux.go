//go:build linux

package dpi

import "syscall"

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
