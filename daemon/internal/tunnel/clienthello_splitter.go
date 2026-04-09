package tunnel

import (
	"net"
	"sync"
)

// ClientHelloSplitterConn fragments the first TLS ClientHello write into two
// TCP writes. This helps evade middleboxes that inspect only the first packet.
type ClientHelloSplitterConn struct {
	net.Conn
	firstWriteOnly sync.Once
	splitAt        int
}

func NewClientHelloSplitterConn(conn net.Conn, splitAt int) *ClientHelloSplitterConn {
	if splitAt <= 0 {
		splitAt = 32
	}
	return &ClientHelloSplitterConn{
		Conn:    conn,
		splitAt: splitAt,
	}
}

func (c *ClientHelloSplitterConn) Write(p []byte) (int, error) {
	var (
		n   int
		err error
	)
	c.firstWriteOnly.Do(func() {
		if len(p) <= c.splitAt {
			n, err = c.Conn.Write(p)
			return
		}
		n1, e1 := c.Conn.Write(p[:c.splitAt])
		if e1 != nil {
			n, err = n1, e1
			return
		}
		n2, e2 := c.Conn.Write(p[c.splitAt:])
		n, err = n1+n2, e2
	})
	if n > 0 || err != nil {
		return n, err
	}
	return c.Conn.Write(p)
}
