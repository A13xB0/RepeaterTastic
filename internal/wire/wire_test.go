package wire

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// RFC 3610 packet vector #1 (M=8, L=2, 13-byte nonce, AAD).
func TestCCMRFC3610(t *testing.T) {
	key := mustHex("C0C1C2C3C4C5C6C7C8C9CACBCCCDCECF")
	nonce := mustHex("00000003020100A0A1A2A3A4A5")
	aad := mustHex("0001020304050607")
	pt := mustHex("08090A0B0C0D0E0F101112131415161718191A1B1C1D1E")
	want := mustHex("588C979A61C663D2F066D0C2C0F989806D5F6B61DAC38417E8D12CFDF926E0")
	c, _ := newCCM(key, 8)
	got := c.Seal(nonce, pt, aad)
	if !bytes.Equal(got, want) {
		t.Fatalf("seal\n got %x\nwant %x", got, want)
	}
	back, err := c.Open(nonce, got, aad)
	if err != nil || !bytes.Equal(back, pt) {
		t.Fatalf("open: %v", err)
	}
	got[3] ^= 1
	if _, err := c.Open(nonce, got, aad); err == nil {
		t.Fatal("tamper not detected")
	}
}

// RFC 3610 packet vector #2 (different length, crosses a block boundary).
func TestCCMRFC3610Vec2(t *testing.T) {
	key := mustHex("C0C1C2C3C4C5C6C7C8C9CACBCCCDCECF")
	nonce := mustHex("00000004030201A0A1A2A3A4A5")
	aad := mustHex("0001020304050607")
	pt := mustHex("08090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F")
	want := mustHex("72C91A36E135F8CF291CA894085C87E3CC15C439C9E43A3BA091D56E10400916")
	c, _ := newCCM(key, 8)
	if got := c.Seal(nonce, pt, aad); !bytes.Equal(got, want) {
		t.Fatalf("seal\n got %x\nwant %x", got, want)
	}
}

func TestExpandPSKAndHash(t *testing.T) {
	if !bytes.Equal(ExpandPSK([]byte{1}), DefaultPSK) {
		t.Fatal("AQ== expansion")
	}
	if ExpandPSK([]byte{2})[15] != DefaultPSK[15]+1 || ExpandPSK([]byte{0}) != nil {
		t.Fatal("short psk variants")
	}
	if h := ChannelHash("LongFast", ExpandPSK([]byte{1}), false); h != 0x08 {
		t.Fatalf("LongFast hash %#x", h)
	}
}

func TestCTRIVLayout(t *testing.T) {
	iv := nonce16(0x11223344, 0xAABBCCDD)
	if hex.EncodeToString(iv[:]) != "ddccbbaa000000004433221100000000" {
		t.Fatalf("iv %x", iv)
	}
	data := bytes.Repeat([]byte("meshtastic"), 20)
	ct := AESCTR(DefaultPSK, 1, 2, data)
	if bytes.Equal(ct, data) || !bytes.Equal(AESCTR(DefaultPSK, 1, 2, ct), data) {
		t.Fatal("ctr roundtrip")
	}
}

func TestPKI(t *testing.T) {
	a, b := GeneratePrivateKey(), GeneratePrivateKey()
	pa, _ := PublicKey(a)
	pbk, _ := PublicKey(b)
	ct, err := PKIEncrypt(a, pbk, 0x1234, 0x5678, []byte("secret dm"), 0xDEADBEEF)
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != 9+12 || binary.LittleEndian.Uint32(ct[len(ct)-4:]) != 0xDEADBEEF {
		t.Fatalf("layout %x", ct)
	}
	if p, ok := PKIDecrypt(b, pa, 0x1234, 0x5678, ct); !ok || string(p) != "secret dm" {
		t.Fatal("decrypt")
	}
	if _, ok := PKIDecrypt(b, pa, 0x1235, 0x5678, ct); ok {
		t.Fatal("wrong nonce accepted")
	}
}

func TestHeaderRoundtrip(t *testing.T) {
	p := &pb.MeshPacket{
		To: Broadcast, From: 0xA1C40E07, Id: 0x0BADF00D, HopLimit: 3, HopStart: 3, WantAck: true,
		Channel: 8, RelayNode: 7, PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: []byte{1, 2, 3}},
	}
	f, err := EncodeFrame(p)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(f[:12]) != "ffffffff070ec4a10df0ad0b" || f[12] != 3|0x08|3<<5 {
		t.Fatalf("frame %x", f)
	}
	q := DecodeFrame(f, -90, 6.25)
	q.RxRssi, q.RxSnr, q.TransportMechanism = nil, 0, 0
	if !proto.Equal(p, q) {
		t.Fatalf("roundtrip\n%v\n%v", p, q)
	}
	if DecodeFrame(append(make([]byte, 8), make([]byte, 9)...), 0, 0) != nil {
		t.Fatal("from=0 accepted")
	}
}

func TestNodeID(t *testing.T) {
	n, err := ParseNodeID("!a1c40e07")
	if err != nil || n != 0xa1c40e07 || NodeID(n) != "!a1c40e07" || LastByte(0x100) != 0xFF {
		t.Fatal("node id helpers")
	}
}
