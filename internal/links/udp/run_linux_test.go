package udp

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"
)

// multicastSender returns a socket that sends multicast out of the loopback interface only.
func multicastSender(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	rc, err := c.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var serr error
	err = rc.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInet4Addr(int(fd), syscall.IPPROTO_IP, syscall.IP_MULTICAST_IF, [4]byte{127, 0, 0, 1})
	})
	if err != nil || serr != nil {
		t.Skipf("cannot pin multicast to loopback: %v %v", err, serr)
	}
	return c
}

func freePort(t *testing.T) int {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).Port
}

func TestRunOnLoopbackMulticast(t *testing.T) {
	h, tap := newTestHost(t)
	port := freePort(t)
	l, err := New(h, []string{"239.255.69.1"}, port, "lo", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- l.Run(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for !l.Connected() {
		if time.Now().After(deadline) {
			cancel()
			t.Skipf("could not join a multicast group on lo: %v", <-res)
		}
		time.Sleep(10 * time.Millisecond)
	}
	tx := multicastSender(t)
	if _, err := tx.WriteToUDP(marshal(t, encPacket(0xabcd, 9)), l.groups[0]); err != nil {
		t.Fatal(err)
	}
	if p := tap.wait(t); p.From != 0xabcd || p.Id != 9 {
		t.Fatalf("got %v", p)
	}
	cancel()
	select {
	case err := <-res:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	if l.Connected() {
		t.Fatal("still connected after stop")
	}
}

func TestRunWithoutJoinableGroup(t *testing.T) {
	// 127.0.0.1 is not a multicast group: every join fails and Run gives up at once.
	l, err := New(nil, []string{"127.0.0.1"}, freePort(t), "", quietLog())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- l.Run(context.Background()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run reported success with no group joined")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run blocked with no groups joined")
	}
	if l.Connected() || len(l.conns) != 0 {
		t.Fatal("link up without a group")
	}
}
