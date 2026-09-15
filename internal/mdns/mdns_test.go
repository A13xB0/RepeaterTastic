package mdns

import (
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"testing"
)

func query(name string, qtype uint16) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[4:], 1)
	b = append(b, encodeName(name)...)
	var t [4]byte
	binary.BigEndian.PutUint16(t[0:], qtype)
	binary.BigEndian.PutUint16(t[2:], classIN)
	return append(b, t[:]...)
}

func TestAnswerPTR(t *testing.T) {
	r := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.hostname = "rpt.local."
	r.SetServices([]Service{{Instance: "Base Camp (!274d0520)", Port: 4403,
		TXT: map[string]string{"id": "!274d0520", "shortname": "BASE", "pio_env": "repeatertastic"}}})
	qs, ok := parseQuestions(query(serviceType, typePTR))
	if !ok || len(qs) != 1 || qs[0].name != serviceType {
		t.Fatalf("parse %v %v", qs, ok)
	}
	resp := r.answer(qs, net.IPv4(192, 168, 1, 20))
	if resp == nil {
		t.Fatal("no answer")
	}
	an := binary.BigEndian.Uint16(resp[6:])
	ar := binary.BigEndian.Uint16(resp[10:])
	if an != 1 || ar != 3 { // PTR; SRV, TXT, A
		t.Fatalf("answers=%d additional=%d", an, ar)
	}
	name, _, ok := readName(resp, 12)
	if !ok || name != serviceType {
		t.Fatalf("answer name %q", name)
	}
	if r.answer([]question{{name: "_other._tcp.local.", qtype: typePTR}}, nil) != nil {
		t.Fatal("answered someone else's service")
	}
}
