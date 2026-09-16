package wire

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"hash/crc32"
	"sync"
)

// DefaultPSK is what a 1-byte PSK of 0x01 ("AQ==") expands to.
var DefaultPSK = []byte{0xd4, 0xf1, 0xbb, 0x3a, 0x20, 0x29, 0x07, 0x59, 0xf0, 0xbc, 0xff, 0xab, 0xcf, 0x4e, 0x69, 0x01}

const (
	pkiTagLen  = 8
	aeadTagLen = 12
)

// ExpandPSK turns ChannelSettings.psk into the AES key (nil means no encryption).
// Mirrors Channels::getKey.
func ExpandPSK(psk []byte) []byte {
	switch n := len(psk); {
	case n == 0:
		return nil
	case n == 1:
		if psk[0] == 0 {
			return nil
		}
		k := append([]byte(nil), DefaultPSK...)
		k[15] += psk[0] - 1
		return k
	case n < 16:
		k := make([]byte, 16)
		copy(k, psk)
		return k
	case n > 16 && n < 32:
		k := make([]byte, 32)
		copy(k, psk)
		return k
	case n > 32:
		return append([]byte(nil), psk[:32]...)
	default:
		return append([]byte(nil), psk...)
	}
}

func xorHash(b []byte) byte {
	var h byte
	for _, c := range b {
		h ^= c
	}
	return h
}

// ChannelHash is the 1-byte header hash; name must already be resolved (empty → preset display name).
func ChannelHash(name string, key []byte, aead bool) uint8 {
	h := xorHash([]byte(name)) ^ xorHash(key)
	if aead {
		h ^= 0xAE
	}
	return h
}

func nonce16(from, id uint32) [16]byte {
	var iv [16]byte
	binary.LittleEndian.PutUint64(iv[0:], uint64(id))
	binary.LittleEndian.PutUint32(iv[8:], from)
	return iv
}

// AESCTR encrypts or decrypts a channel payload. Firmware counts in the last 4 IV bytes (big-endian),
// which matches a full 128-bit CTR for any packet Meshtastic can carry.
func AESCTR(key []byte, from, id uint32, data []byte) []byte {
	out := make([]byte, len(data))
	if len(key) == 0 {
		copy(out, data)
		return out
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		copy(out, data)
		return out
	}
	iv := nonce16(from, id)
	cipher.NewCTR(b, iv[:]).XORKeyStream(out, data)
	return out
}

func ccmNonce(from, id, extra uint32) []byte {
	n := nonce16(from, id)
	if extra != 0 {
		binary.LittleEndian.PutUint32(n[4:], extra)
	}
	return n[:13]
}

// ---------------------------------------------------------------------------------------- PKI

// GeneratePrivateKey returns a fresh X25519 private key.
func GeneratePrivateKey() []byte {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return ClampPrivateKey(k.Bytes())
}

// ClampPrivateKey returns the X25519 private key in clamped form. The public key (and so the node
// number) is the same; meshtasticd 2.8 replaces an unclamped saved key with a new one when it
// boots.
func ClampPrivateKey(priv []byte) []byte {
	if len(priv) != 32 {
		return priv
	}
	k := append([]byte(nil), priv...)
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return k
}

// Clamped reports whether an X25519 private key is in clamped form.
func Clamped(priv []byte) bool {
	return len(priv) == 32 && priv[0]&7 == 0 && priv[31]&128 == 0 && priv[31]&64 != 0
}

// PublicKey derives the X25519 public key.
func PublicKey(priv []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

var sharedCache sync.Map // [64]byte → []byte

// SharedKey is SHA-256(X25519(priv, peer)), as CryptoEngine::setDHPublicKey + hash.
func SharedKey(priv, peer []byte) ([]byte, error) {
	var ck [64]byte
	copy(ck[:32], priv)
	copy(ck[32:], peer)
	if v, ok := sharedCache.Load(ck); ok {
		return v.([]byte), nil
	}
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	pk, err := ecdh.X25519().NewPublicKey(peer)
	if err != nil {
		return nil, err
	}
	secret, err := k.ECDH(pk)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(secret)
	sharedCache.Store(ck, sum[:])
	return sum[:], nil
}

// PKIEncrypt returns ciphertext || tag(8) || extraNonce(4 LE). extra 0 picks a random nonce.
func PKIEncrypt(priv, peer []byte, from, id uint32, plain []byte, extra uint32) ([]byte, error) {
	key, err := SharedKey(priv, peer)
	if err != nil {
		return nil, err
	}
	for extra == 0 {
		var b [4]byte
		_, _ = rand.Read(b[:])
		extra = binary.LittleEndian.Uint32(b[:])
	}
	c, _ := newCCM(key, pkiTagLen)
	out := c.Seal(ccmNonce(from, id, extra), plain, nil)
	return binary.LittleEndian.AppendUint32(out, extra), nil
}

// PKIDecrypt reverses PKIEncrypt; ok is false when authentication fails.
func PKIDecrypt(priv, peer []byte, from, id uint32, data []byte) ([]byte, bool) {
	if len(data) <= pkiTagLen+4 {
		return nil, false
	}
	key, err := SharedKey(priv, peer)
	if err != nil {
		return nil, false
	}
	extra := binary.LittleEndian.Uint32(data[len(data)-4:])
	c, _ := newCCM(key, pkiTagLen)
	p, err := c.Open(ccmNonce(from, id, extra), data[:len(data)-4], nil)
	return p, err == nil
}

// ------------------------------------------------------------------------------ AEAD channels

func aad(from, to uint32) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint32(b, from)
	binary.LittleEndian.PutUint32(b[4:], to)
	return b
}

// AEADEncrypt is the AES-CCM channel mode on firmware master (ChannelSettings.use_aead).
func AEADEncrypt(key []byte, from, to, id uint32, plain []byte) ([]byte, error) {
	c, err := newCCM(key, aeadTagLen)
	if err != nil {
		return nil, err
	}
	return c.Seal(ccmNonce(from, id, 0), plain, aad(from, to)), nil
}

// AEADDecrypt reverses AEADEncrypt.
func AEADDecrypt(key []byte, from, to, id uint32, data []byte) ([]byte, bool) {
	c, err := newCCM(key, aeadTagLen)
	if err != nil {
		return nil, false
	}
	p, err := c.Open(ccmNonce(from, id, 0), data, aad(from, to))
	return p, err == nil
}

// NodeNumFromPublicKey is the 2.8+ node number: CRC-32 (IEEE) of the public key.
func NodeNumFromPublicKey(pub []byte) uint32 { return crc32.ChecksumIEEE(pub) }

// RandomPacketID returns a non-zero random packet id.
func RandomPacketID() uint32 {
	var b [4]byte
	for {
		_, _ = rand.Read(b[:])
		if v := binary.LittleEndian.Uint32(b[:]); v != 0 {
			return v
		}
	}
}

// IsPublicKey reports whether an expanded channel key is none or one of the well-known
// default keys (psk 0x01-0xff expand to DefaultPSK with a different last byte).
func IsPublicKey(key []byte) bool {
	return len(key) == 0 || len(key) == len(DefaultPSK) && string(key[:15]) == string(DefaultPSK[:15])
}
