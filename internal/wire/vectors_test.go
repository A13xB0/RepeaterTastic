package wire

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/pb"
)

// Golden vectors captured from real meshtasticd (tests/interop). Every packet must decode with our
// crypto exactly as the firmware produced it, and PKI packets must re-encrypt to identical bytes.
type vector struct {
	Name     string `json:"name"`
	Firmware string `json:"firmware_version"`
	UDP      struct {
		DatagramHex string `json:"datagram_hex"`
	} `json:"udp"`
	From         uint32 `json:"from"`
	To           uint32 `json:"to"`
	ID           uint32 `json:"id"`
	ChannelHash  uint32 `json:"channel_hash"`
	RadioHeader  string `json:"derived_radio_header_hex"`
	EncryptedHex string `json:"encrypted_hex"`
	PlaintextHex string `json:"plaintext_hex"`
	Crypto       struct {
		Type          string `json:"type"`
		ChannelName   string `json:"channel_name"`
		ChannelKeyHex string `json:"channel_key_hex"`
		ChannelHash   uint32 `json:"channel_hash"`
		SenderPriv    string `json:"sender_private_key_hex"`
		SenderPub     string `json:"sender_public_key_hex"`
		ReceiverPriv  string `json:"receiver_private_key_hex"`
		ReceiverPub   string `json:"receiver_public_key_hex"`
		AESKey        string `json:"aes_key_hex"`
		ExtraNonceU32 uint32 `json:"extra_nonce_u32_le"`
	} `json:"crypto"`
}

func TestGoldenVectors(t *testing.T) {
	files, _ := filepath.Glob("../../tests/interop/vectors/*/*.json")
	n := 0
	for _, f := range files {
		if filepath.Base(f) == "index.json" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var v vector
		if err := json.Unmarshal(b, &v); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		t.Run(v.Firmware+"/"+v.Name, func(t *testing.T) {
			dg := mustHex(v.UDP.DatagramHex)
			p := &pb.MeshPacket{}
			if err := proto.Unmarshal(dg, p); err != nil {
				t.Fatal(err)
			}
			frame, err := EncodeFrame(p)
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(frame[:HeaderLen]) != v.RadioHeader {
				t.Fatalf("radio header %x, want %s", frame[:HeaderLen], v.RadioHeader)
			}
			back := DecodeFrame(frame, 0, 0)
			if back.From != v.From || back.To != v.To || back.Id != v.ID || !bytes.Equal(back.GetEncrypted(), mustHex(v.EncryptedHex)) {
				t.Fatal("frame decode mismatch")
			}
			enc := p.GetEncrypted()
			want := mustHex(v.PlaintextHex)
			switch v.Crypto.Type {
			case "channel_aes_ctr":
				key := mustHex(v.Crypto.ChannelKeyHex)
				if h := ChannelHash(v.Crypto.ChannelName, key, false); uint32(h) != v.Crypto.ChannelHash || uint32(h) != p.Channel {
					t.Fatalf("channel hash %#x, want %#x", h, v.Crypto.ChannelHash)
				}
				if got := AESCTR(key, p.From, p.Id, enc); !bytes.Equal(got, want) {
					t.Fatalf("plaintext\n got %x\nwant %x", got, want)
				}
			case "pki_x25519_aes_ccm":
				rpriv, spub := mustHex(v.Crypto.ReceiverPriv), mustHex(v.Crypto.SenderPub)
				k, err := SharedKey(rpriv, spub)
				if err != nil || hex.EncodeToString(k) != v.Crypto.AESKey {
					t.Fatalf("shared key %x want %s (%v)", k, v.Crypto.AESKey, err)
				}
				got, ok := PKIDecrypt(rpriv, spub, p.From, p.Id, enc)
				if !ok || !bytes.Equal(got, want) {
					t.Fatalf("pki decrypt ok=%v\n got %x\nwant %x", ok, got, want)
				}
				re, err := PKIEncrypt(mustHex(v.Crypto.SenderPriv), mustHex(v.Crypto.ReceiverPub), p.From, p.Id, want,
					v.Crypto.ExtraNonceU32)
				if err != nil || !bytes.Equal(re, enc) {
					t.Fatalf("pki re-encrypt\n got %x\nwant %x", re, enc)
				}
				if v.Firmware >= "2.8" {
					if NodeNumFromPublicKey(spub) != p.From {
						t.Fatalf("crc32(pubkey) %08x != from %08x", NodeNumFromPublicKey(spub), p.From)
					}
				}
			default:
				t.Skipf("crypto type %q", v.Crypto.Type)
			}
			d := &pb.Data{}
			if err := proto.Unmarshal(want, d); err != nil || d.Portnum == pb.PortNum_UNKNOWN_APP {
				t.Fatalf("plaintext is not a Data protobuf: %v", err)
			}
		})
		n++
	}
	if n == 0 {
		t.Skip("no vectors found")
	}
}
