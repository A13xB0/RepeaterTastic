package mdns

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// sink stands in for the multicast group: announcements and group replies land here.
var sink *net.UDPConn

func TestMain(m *testing.M) {
	var err error
	sink, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		panic(err)
	}
	group = sink.LocalAddr().(*net.UDPAddr)
	announceDelay, announceRepeat, announceInterval = time.Millisecond, time.Millisecond, time.Hour
	code := m.Run()
	sink.Close()
	os.Exit(code)
}

func testResponder() *Responder {
	r := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.hostname = "rpt.local."
	r.SetServices([]Service{{Instance: "Base.Camp", Port: 4403, TXT: map[string]string{"id": "!1"}}})
	return r
}

func withHostname(t *testing.T, name string, err error) {
	t.Helper()
	old := osHostname
	osHostname = func() (string, error) { return name, err }
	t.Cleanup(func() { osHostname = old })
}

func TestNewHostname(t *testing.T) {
	cases := []struct {
		host string
		err  error
		want string
	}{
		{"pi.lan", nil, "pi-lan.local."},
		{"box.local", nil, "box.local."},
		{"", errors.New("no name"), "repeatertastic.local."},
	}
	for _, c := range cases {
		withHostname(t, c.host, c.err)
		if got := New(slog.Default()).hostname; got != c.want {
			t.Errorf("host %q: hostname %q, want %q", c.host, got, c.want)
		}
	}
}

func TestSetServicesDoesNotBlock(t *testing.T) {
	r := testResponder()
	for i := 0; i < 3; i++ {
		r.SetServices(nil)
	}
	if r.answer([]question{{name: serviceType, qtype: typePTR}}, nil) != nil {
		t.Fatal("answered with no services")
	}
}

func counts(t *testing.T, resp []byte) (an, ar uint16) {
	t.Helper()
	if len(resp) < 12 {
		t.Fatalf("short response %x", resp)
	}
	return binary.BigEndian.Uint16(resp[6:]), binary.BigEndian.Uint16(resp[10:])
}

func TestAnswerQuestionKinds(t *testing.T) {
	ip := net.IPv4(10, 0, 0, 2)
	cases := []struct {
		name       string
		q          question
		ip         net.IP
		wantNil    bool
		wantAn     uint16
		wantExtras uint16
	}{
		{"meta", question{metaQuery, typePTR}, ip, false, 1, 0},
		{"meta wrong type", question{metaQuery, typeA}, ip, true, 0, 0},
		{"host A", question{"RPT.local.", typeA}, ip, false, 1, 0},
		{"host ANY", question{"rpt.local.", typeANY}, ip, false, 1, 0},
		{"host A without address", question{"rpt.local.", typeA}, nil, false, 0, 0},
		{"instance", question{"base\\.camp._meshtastic._tcp.local.", typeSRV}, ip, false, 0, 3},
		{"instance no ip", question{instanceName(Service{Instance: "Base.Camp"}), typeTXT}, nil, false, 0, 2},
		{"unknown", question{"x.local.", typeA}, ip, true, 0, 0},
	}
	r := testResponder()
	for _, c := range cases {
		resp := r.answer([]question{c.q}, c.ip)
		if c.wantNil {
			if resp != nil {
				t.Errorf("%s: unexpected answer", c.name)
			}
			continue
		}
		if an, ar := counts(t, resp); an != c.wantAn || ar != c.wantExtras {
			t.Errorf("%s: an=%d ar=%d, want %d %d", c.name, an, ar, c.wantAn, c.wantExtras)
		}
	}
}

func TestAnswerHostAddress(t *testing.T) {
	r := testResponder()
	resp := r.answer([]question{{"rpt.local.", typeA}}, net.IPv4(10, 1, 2, 3))
	if !strings.HasSuffix(string(resp), string([]byte{0, 4, 10, 1, 2, 3})) {
		t.Fatalf("A record missing address: %x", resp)
	}
}

func TestEncodeName(t *testing.T) {
	if got := encodeName("a\\.b.c."); string(got) != "\x03a.b\x01c\x00" {
		t.Fatalf("escaped: %q", got)
	}
	long := strings.Repeat("x", 70)
	got := encodeName(long + ".local")
	if got[0] != 63 || got[64] != 5 || string(got[65:70]) != "local" {
		t.Fatalf("long label not truncated: %q", got)
	}
	if got := encodeName("trailing\\"); string(got) != "\x09trailing\\\x00" {
		t.Fatalf("trailing backslash: %q", got)
	}
}

func TestEncodeTXT(t *testing.T) {
	if got := encodeTXT(nil); string(got) != "\x00" {
		t.Fatalf("empty: %q", got)
	}
	got := encodeTXT(map[string]string{"fw": strings.Repeat("v", 300), "other": "ignored", "id": "!1"})
	if got[0] != 5 || string(got[1:6]) != "id=!1" || got[6] != 255 || len(got) != 6+1+255 {
		t.Fatalf("txt %q", got)
	}
}

func TestParseQuestionsRejects(t *testing.T) {
	resp := query(serviceType, typePTR)
	resp[2] = 0x84
	truncated := query(serviceType, typePTR)
	truncated = truncated[:len(truncated)-2]
	two := query(serviceType, typePTR)
	two[5] = 2
	cases := []struct {
		name  string
		b     []byte
		n     int
		query bool
	}{
		{"short", []byte{1, 2, 3}, 0, false},
		{"response", resp, 0, false},
		{"truncated", truncated, 0, false},
		{"second missing", two, 1, true},
	}
	for _, c := range cases {
		qs, ok := parseQuestions(c.b)
		if len(qs) != c.n || ok != c.query {
			t.Errorf("%s: %v %v", c.name, qs, ok)
		}
	}
}

func TestReadNameCompression(t *testing.T) {
	// "local." at 12, then "foo" + pointer to 12 at 19.
	b := append(make([]byte, 12), "\x05local\x00\x03foo\xc0\x0c"...)
	name, end, ok := readName(b, 19)
	if !ok || name != "foo.local." || end != len(b) {
		t.Fatalf("got %q %d %v", name, end, ok)
	}
	dotted := []byte("\x03a.b\x00")
	if name, _, _ := readName(dotted, 0); name != "a\\.b." {
		t.Fatalf("dotted label %q", name)
	}
	loop := []byte{0xc0, 0x00}
	bad := map[string][]byte{
		"past end":           {},
		"cut pointer":        {0xc0},
		"label past end":     {5, 'a'},
		"pointer loop":       loop,
		"pointer past end":   {0xc0, 0x09},
		"missing terminator": {1, 'a'},
	}
	for what, b := range bad {
		if _, _, ok := readName(b, 0); ok {
			t.Errorf("%s: accepted", what)
		}
	}
}

func TestLocalIPv4For(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skip(err)
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil || ipn.IP.IsLoopback() {
			continue
		}
		if got := localIPv4For(ipn.IP); !got.Equal(ipn.IP) {
			t.Fatalf("peer %v: got %v", ipn.IP, got)
		}
		if localIPv4For(nil) == nil {
			t.Fatal("no default address")
		}
		return
	}
	if got := localIPv4For(net.IPv4(192, 0, 2, 1)); got != nil {
		t.Fatalf("no interfaces but got %v", got)
	}
}

func TestRunListenError(t *testing.T) {
	old := listenMulticast
	listenMulticast = func() (*net.UDPConn, error) { return nil, errors.New("no multicast") }
	t.Cleanup(func() { listenMulticast = old })
	if err := testResponder().Run(context.Background()); err == nil || err.Error() != "no multicast" {
		t.Fatalf("err %v", err)
	}
}

func readPacket(t *testing.T, c *net.UDPConn) []byte {
	t.Helper()
	buf := make([]byte, 2048)
	if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err := c.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return buf[:n]
}

func startRun(t *testing.T, r *Responder) (*net.UDPAddr, context.CancelFunc, chan error) {
	t.Helper()
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	old := listenMulticast
	listenMulticast = func() (*net.UDPConn, error) { return srv, nil }
	t.Cleanup(func() { listenMulticast = old })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	return srv.LocalAddr().(*net.UDPAddr), cancel, done
}

func TestRunAnnouncesAndAnswers(t *testing.T) {
	r := testResponder()
	addr, cancel, done := startRun(t, r)

	// Startup announcement, then two more after SetServices (one pending from testResponder).
	for i := 0; i < 3; i++ {
		if an, _ := counts(t, readPacket(t, sink)); an != 1 {
			t.Fatalf("announcement %d: an=%d", i, an)
		}
	}

	client, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	notQuery := query(serviceType, typePTR)
	notQuery[2] = 0x84
	for _, pkt := range [][]byte{{0}, notQuery, query("nobody.local.", typeA), query(serviceType, typePTR)} {
		if _, err := client.Write(pkt); err != nil {
			t.Fatal(err)
		}
	}
	// Only the last packet gets a reply, sent straight back (legacy unicast).
	name, _, ok := readName(readPacket(t, client), 12)
	if !ok || name != serviceType {
		t.Fatalf("reply name %q", name)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
