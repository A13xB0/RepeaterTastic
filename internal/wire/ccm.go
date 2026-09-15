package wire

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/subtle"
	"encoding/binary"
	"errors"
)

// ccm implements AES-CCM (RFC 3610) for the nonce and tag sizes Meshtastic uses. The Go standard
// library has no CCM, and pulling in a module for ~80 lines isn't worth it.
type ccm struct {
	b   cipher.Block
	tag int
}

var errOpen = errors.New("ccm: message authentication failed")

func newCCM(key []byte, tagLen int) (*ccm, error) {
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if tagLen < 4 || tagLen > 16 || tagLen%2 != 0 {
		return nil, errors.New("ccm: invalid tag length")
	}
	return &ccm{b: b, tag: tagLen}, nil
}

func (c *ccm) mac(nonce, plain, aad []byte) []byte {
	L := 15 - len(nonce)
	var b0 [16]byte
	flags := byte((c.tag-2)/2<<3 | (L - 1))
	if len(aad) > 0 {
		flags |= 0x40
	}
	b0[0] = flags
	copy(b0[1:], nonce)
	n := len(plain)
	for i := 15; i > 15-L; i-- {
		b0[i] = byte(n)
		n >>= 8
	}
	x := make([]byte, 16)
	xorBlock := func(block []byte) {
		for i := range block {
			x[i] ^= block[i]
		}
		c.b.Encrypt(x, x)
	}
	xorBlock(b0[:])
	if len(aad) > 0 {
		// Meshtastic AAD is 8 bytes, so the 2-byte length form is always enough.
		buf := make([]byte, 2, 2+len(aad)+15)
		binary.BigEndian.PutUint16(buf, uint16(len(aad)))
		buf = append(buf, aad...)
		for len(buf)%16 != 0 {
			buf = append(buf, 0)
		}
		for i := 0; i < len(buf); i += 16 {
			xorBlock(buf[i : i+16])
		}
	}
	var blk [16]byte
	for i := 0; i < len(plain); i += 16 {
		blk = [16]byte{}
		copy(blk[:], plain[i:])
		xorBlock(blk[:])
	}
	return x[:c.tag]
}

func (c *ccm) ctr(nonce []byte, counter uint32, dst, src []byte) {
	L := 15 - len(nonce)
	var a, s [16]byte
	a[0] = byte(L - 1)
	copy(a[1:], nonce)
	for off := 0; off < len(src); off += 16 {
		ctr := counter
		for i := 15; i > 15-L; i-- {
			a[i] = byte(ctr)
			ctr >>= 8
		}
		c.b.Encrypt(s[:], a[:])
		end := off + 16
		if end > len(src) {
			end = len(src)
		}
		for i := off; i < end; i++ {
			dst[i] = src[i] ^ s[i-off]
		}
		counter++
	}
}

// Seal returns ciphertext || tag.
func (c *ccm) Seal(nonce, plain, aad []byte) []byte {
	t := c.mac(nonce, plain, aad)
	out := make([]byte, len(plain)+c.tag)
	c.ctr(nonce, 1, out[:len(plain)], plain)
	c.ctr(nonce, 0, out[len(plain):], t)
	return out
}

// Open verifies and decrypts ciphertext || tag.
func (c *ccm) Open(nonce, data, aad []byte) ([]byte, error) {
	if len(data) < c.tag {
		return nil, errOpen
	}
	ct, tag := data[:len(data)-c.tag], data[len(data)-c.tag:]
	plain := make([]byte, len(ct))
	c.ctr(nonce, 1, plain, ct)
	want := make([]byte, c.tag)
	c.ctr(nonce, 0, want, tag)
	if subtle.ConstantTimeCompare(want, c.mac(nonce, plain, aad)) != 1 {
		return nil, errOpen
	}
	return plain, nil
}
