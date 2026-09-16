package kiss

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// TCPScheme prefixes a Device reached over TCP instead of a serial port, e.g. tcp://127.0.0.1:4405
// for meshtasticd in raw modem mode (see docs/meshtasticd-raw-modem.md).
const TCPScheme = "tcp://"

// TCPAddr reports whether dev is a tcp:// device and returns its host:port. A tcp:// device
// without a usable host:port returns an error.
func TCPAddr(dev string) (addr string, ok bool, err error) {
	rest, ok := strings.CutPrefix(dev, TCPScheme)
	if !ok {
		return "", false, nil
	}
	host, port, err := net.SplitHostPort(rest)
	if err != nil || host == "" || port == "" {
		return "", true, fmt.Errorf("kiss: device %q must be tcp://host:port", dev)
	}
	return net.JoinHostPort(host, port), true, nil
}

// dialTCP connects to a modem served over TCP. Keepalives notice a peer that vanished without
// closing the connection, so the supervisor can redial it.
func dialTCP(addr string, timeout time.Duration) (io.ReadWriteCloser, error) {
	d := net.Dialer{Timeout: timeout, KeepAlive: 15 * time.Second}
	c, err := d.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
	return c, nil
}
