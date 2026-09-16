package wire

import (
	"bytes"
	"testing"
)

func TestExpandPSKLengths(t *testing.T) {
	long := bytes.Repeat([]byte{7}, 40)
	cases := []struct {
		name    string
		psk     []byte
		wantLen int
		prefix  []byte
	}{
		{"empty", nil, 0, nil},
		{"zero byte", []byte{0}, 0, nil},
		{"short padded to 16", []byte{1, 2, 3}, 16, []byte{1, 2, 3, 0}},
		{"exact 16", bytes.Repeat([]byte{5}, 16), 16, bytes.Repeat([]byte{5}, 16)},
		{"between 16 and 32 padded", bytes.Repeat([]byte{6}, 20), 32, append(bytes.Repeat([]byte{6}, 20), 0)},
		{"exact 32", bytes.Repeat([]byte{8}, 32), 32, bytes.Repeat([]byte{8}, 32)},
		{"over 32 truncated", long, 32, long[:32]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExpandPSK(tc.psk)
			if len(got) != tc.wantLen || !bytes.HasPrefix(got, tc.prefix) {
				t.Fatalf("ExpandPSK(%x) = %x", tc.psk, got)
			}
		})
	}
}

func TestExpandPSKDoesNotAlias(t *testing.T) {
	psk := bytes.Repeat([]byte{9}, 32)
	got := ExpandPSK(psk)
	got[0] = 0
	if psk[0] != 9 {
		t.Fatal("ExpandPSK returned the caller's slice")
	}
	def := ExpandPSK([]byte{1})
	def[0] ^= 0xFF
	if DefaultPSK[0] != 0xd4 {
		t.Fatal("ExpandPSK aliased DefaultPSK")
	}
}

func TestChannelHashAEADFlag(t *testing.T) {
	plain := ChannelHash("x", []byte{1}, false)
	if got := ChannelHash("x", []byte{1}, true); got != plain^0xAE {
		t.Fatalf("aead hash %#x, plain %#x", got, plain)
	}
}

func TestAESCTRWithoutUsableKey(t *testing.T) {
	data := []byte("clear text")
	for _, key := range [][]byte{nil, {1, 2, 3}} {
		out := AESCTR(key, 1, 2, data)
		if !bytes.Equal(out, data) {
			t.Fatalf("key %x: got %q, want passthrough", key, out)
		}
		out[0] = 'X'
		if data[0] != 'c' {
			t.Fatal("AESCTR output aliases input")
		}
	}
}

func TestClampedKeys(t *testing.T) {
	k := GeneratePrivateKey()
	if !Clamped(k) {
		t.Fatalf("generated key not clamped: %x", k)
	}
	raw := bytes.Repeat([]byte{0xFF}, 32)
	if Clamped(raw) {
		t.Fatal("0xff.. reported clamped")
	}
	c := ClampPrivateKey(raw)
	if !Clamped(c) || raw[0] != 0xFF {
		t.Fatalf("clamp %x (input %x)", c, raw)
	}
	pr, _ := PublicKey(raw)
	pc, _ := PublicKey(c)
	if !bytes.Equal(pr, pc) {
		t.Fatal("clamping changed the public key")
	}
	short := []byte{1, 2}
	if got := ClampPrivateKey(short); !bytes.Equal(got, short) || Clamped(short) {
		t.Fatal("short key handling")
	}
}

func TestKeyErrors(t *testing.T) {
	good := GeneratePrivateKey()
	pub, _ := PublicKey(good)
	if _, err := PublicKey([]byte{1}); err == nil {
		t.Fatal("PublicKey accepted short key")
	}
	if _, err := SharedKey([]byte{1}, pub); err == nil {
		t.Fatal("SharedKey accepted short private key")
	}
	if _, err := SharedKey(good, []byte{1}); err == nil {
		t.Fatal("SharedKey accepted short peer key")
	}
	if _, err := SharedKey(good, make([]byte, 32)); err == nil {
		t.Fatal("SharedKey accepted low-order peer key")
	}
	if _, err := PKIEncrypt(good, []byte{1}, 1, 2, []byte("x"), 1); err == nil {
		t.Fatal("PKIEncrypt accepted bad peer")
	}
}

func TestSharedKeySymmetricAndCached(t *testing.T) {
	a, b := GeneratePrivateKey(), GeneratePrivateKey()
	pa, _ := PublicKey(a)
	pbk, _ := PublicKey(b)
	k1, err := SharedKey(a, pbk)
	if err != nil {
		t.Fatal(err)
	}
	k2, _ := SharedKey(b, pa)
	k3, _ := SharedKey(a, pbk)
	if len(k1) != 32 || !bytes.Equal(k1, k2) || !bytes.Equal(k1, k3) {
		t.Fatalf("shared keys differ: %x %x %x", k1, k2, k3)
	}
}

func TestPKIRandomNonceAndRejects(t *testing.T) {
	a, b := GeneratePrivateKey(), GeneratePrivateKey()
	pa, _ := PublicKey(a)
	pbk, _ := PublicKey(b)
	ct, err := PKIEncrypt(a, pbk, 1, 2, []byte("hello"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := PKIDecrypt(b, pa, 1, 2, ct); !ok || string(p) != "hello" {
		t.Fatal("random-nonce roundtrip failed")
	}
	if _, ok := PKIDecrypt(b, pa, 1, 2, ct[:pkiTagLen+4]); ok {
		t.Fatal("too-short data accepted")
	}
	if _, ok := PKIDecrypt([]byte{1}, pa, 1, 2, ct); ok {
		t.Fatal("bad private key accepted")
	}
}

func TestAEADRoundtrip(t *testing.T) {
	key := ExpandPSK([]byte{1})
	ct, err := AEADEncrypt(key, 0x10, Broadcast, 0x99, []byte("aead payload"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ct) != len("aead payload")+AEADOverhead {
		t.Fatalf("ciphertext length %d", len(ct))
	}
	if p, ok := AEADDecrypt(key, 0x10, Broadcast, 0x99, ct); !ok || string(p) != "aead payload" {
		t.Fatal("roundtrip failed")
	}
	// The destination is authenticated, so a different "to" must fail.
	if _, ok := AEADDecrypt(key, 0x10, 0x11, 0x99, ct); ok {
		t.Fatal("changed AAD accepted")
	}
	if _, ok := AEADDecrypt(key, 0x10, Broadcast, 0x99, ct[:4]); ok {
		t.Fatal("truncated data accepted")
	}
}

func TestAEADBadKey(t *testing.T) {
	if _, err := AEADEncrypt([]byte{1, 2}, 1, 2, 3, []byte("x")); err == nil {
		t.Fatal("encrypt accepted bad key")
	}
	if _, ok := AEADDecrypt([]byte{1, 2}, 1, 2, 3, make([]byte, 20)); ok {
		t.Fatal("decrypt accepted bad key")
	}
}

func TestNewCCMTagLength(t *testing.T) {
	key := make([]byte, 16)
	for _, n := range []int{2, 5, 18} {
		if _, err := newCCM(key, n); err == nil {
			t.Fatalf("tag length %d accepted", n)
		}
	}
	if _, err := newCCM(key, 16); err != nil {
		t.Fatal(err)
	}
}

func TestRandomPacketID(t *testing.T) {
	seen := map[uint32]bool{}
	for range 50 {
		id := RandomPacketID()
		if id == 0 {
			t.Fatal("zero packet id")
		}
		seen[id] = true
	}
	if len(seen) < 45 {
		t.Fatalf("only %d distinct ids out of 50", len(seen))
	}
}

func TestIsPublicKey(t *testing.T) {
	cases := []struct {
		key  []byte
		want bool
	}{
		{nil, true},
		{ExpandPSK([]byte{1}), true},
		{ExpandPSK([]byte{0x20}), true},
		{bytes.Repeat([]byte{1}, 16), false},
		{append(append([]byte(nil), DefaultPSK...), DefaultPSK...), false},
	}
	for i, tc := range cases {
		if got := IsPublicKey(tc.key); got != tc.want {
			t.Errorf("case %d (%x): got %v", i, tc.key, got)
		}
	}
}
