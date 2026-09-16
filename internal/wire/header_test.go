package wire

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ScotMesh/RepeaterTastic/pb"
)

func encrypted(n int) *pb.MeshPacket_Encrypted {
	return &pb.MeshPacket_Encrypted{Encrypted: bytes.Repeat([]byte{0xAB}, n)}
}

func TestEncodeFrameErrors(t *testing.T) {
	if _, err := EncodeFrame(&pb.MeshPacket{From: 1, PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}); !errors.Is(err, ErrNotEncrypted) {
		t.Fatalf("decoded packet: %v", err)
	}
	if _, err := EncodeFrame(&pb.MeshPacket{PayloadVariant: encrypted(1)}); err == nil {
		t.Fatal("from=0 accepted")
	}
	if _, err := EncodeFrame(&pb.MeshPacket{From: 1, PayloadVariant: encrypted(MaxPayload + 1)}); err == nil {
		t.Fatal("oversized payload accepted")
	}
	if f, err := EncodeFrame(&pb.MeshPacket{From: 1, PayloadVariant: encrypted(MaxPayload)}); err != nil || len(f) != MaxFrame {
		t.Fatalf("max payload: len %d err %v", len(f), err)
	}
}

func TestEncodeFrameFlags(t *testing.T) {
	p := &pb.MeshPacket{From: 1, HopLimit: 9, ViaMqtt: true, PayloadVariant: encrypted(1)}
	f, err := EncodeFrame(p)
	if err != nil {
		t.Fatal(err)
	}
	// An out-of-range hop limit falls back to HopReliable.
	if f[12] != HopReliable|flagViaMQTT {
		t.Fatalf("flags %#x", f[12])
	}
	q := DecodeFrame(f, 0, 0)
	if !q.ViaMqtt || q.HopLimit != HopReliable || q.RxRssi != nil {
		t.Fatalf("decoded %v", q)
	}
	// hop_start 0 means a pre-2.3 sender: next-hop/relay bytes are not trusted.
	if q.NextHop != 0 || q.RelayNode != 0 {
		t.Fatalf("relay bytes read without hop_start: %v", q)
	}
}

func TestDecodeFrameLengths(t *testing.T) {
	for _, n := range []int{0, HeaderLen, MaxFrame + 1} {
		if DecodeFrame(make([]byte, n), 0, 0) != nil {
			t.Errorf("frame of %d bytes accepted", n)
		}
	}
}

func TestLastByte(t *testing.T) {
	if LastByte(0x12345678) != 0x78 || LastByte(0x1200) != 0xFF {
		t.Fatal("LastByte")
	}
}

func TestParseNodeIDForms(t *testing.T) {
	cases := []struct {
		in      string
		want    uint32
		wantErr bool
	}{
		{" !0000002a ", 42, false},
		{"0x2A", 42, false},
		{"0X2a", 42, false},
		{"42", 42, false},
		{"!zz", 0, true},
		{"0x1ffffffff", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := ParseNodeID(tc.in)
		if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
			t.Errorf("ParseNodeID(%q) = %d, %v", tc.in, got, err)
		}
	}
}

func TestHopsAway(t *testing.T) {
	withBitfield := &pb.MeshPacket_Decoded{Decoded: &pb.Data{Bitfield: new(uint32)}}
	cases := []struct {
		name string
		p    *pb.MeshPacket
		want int
	}{
		{"unknown hop start", &pb.MeshPacket{HopLimit: 3, PayloadVariant: encrypted(1)}, -1},
		{"decoded without bitfield", &pb.MeshPacket{PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{}}}, -1},
		{"bitfield means zero start is real", &pb.MeshPacket{PayloadVariant: withBitfield}, 0},
		{"limit above start", &pb.MeshPacket{HopStart: 2, HopLimit: 5}, -1},
		{"two hops", &pb.MeshPacket{HopStart: 5, HopLimit: 3}, 2},
	}
	for _, tc := range cases {
		if got := HopsAway(tc.p); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.name, got, tc.want)
		}
	}
}
