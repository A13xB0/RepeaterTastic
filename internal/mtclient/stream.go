package mtclient

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
)

// Stream protocol framing, shared by TCP (port 4403) and serial: 0x94 0xC3, a big-endian
// 16-bit length, then a ToRadio or FromRadio protobuf.
const (
	start1 = 0x94
	start2 = 0xC3
	// MaxFrame is the largest protobuf the firmware sends or accepts in one frame.
	MaxFrame = 512
)

// wakeup is what the Python client sends before its first frame so a sleeping serial console
// switches to protobuf mode; harmless over TCP.
var wakeup = func() []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = start2
	}
	return b
}()

var errFrameTooBig = errors.New("mtclient: frame larger than 512 bytes")

// AppendFrame appends one framed payload to dst.
func AppendFrame(dst, payload []byte) ([]byte, error) {
	if len(payload) > MaxFrame {
		return dst, errFrameTooBig
	}
	dst = append(dst, start1, start2, byte(len(payload)>>8), byte(len(payload)))
	return append(dst, payload...), nil
}

// ReadFrames calls fn with each frame's payload until fn returns false or r fails. Bytes outside
// frames (a serial console's log text) are skipped, and so are oversize frames.
func ReadFrames(r io.Reader, fn func([]byte) bool) error {
	br := bufio.NewReader(r)
	for {
		payload, ok, err := nextFrame(br)
		if err != nil {
			return err
		}
		if ok && !fn(payload) {
			return nil
		}
	}
}

// nextFrame reads the next frame's payload. ok is false when what it read wasn't a frame (a stray
// byte) or was an oversize one, whose payload is left to be skipped as stray bytes.
func nextFrame(br *bufio.Reader) (payload []byte, ok bool, err error) {
	found, err := readStart(br)
	if !found || err != nil {
		return nil, false, err
	}
	var lenBuf [2]byte
	if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
		return nil, false, err
	}
	n := int(binary.BigEndian.Uint16(lenBuf[:]))
	if n > MaxFrame {
		return nil, false, nil
	}
	payload = make([]byte, n)
	if _, err := io.ReadFull(br, payload); err != nil {
		return nil, false, err
	}
	return payload, true, nil
}

// readStart reads a byte, and the next if it's start1, reporting whether they were the start
// marker. A start1 where start2 should be is put back: it may begin the real marker.
func readStart(br *bufio.Reader) (bool, error) {
	b, err := br.ReadByte()
	if err != nil || b != start1 {
		return false, err
	}
	b, err = br.ReadByte()
	if err != nil {
		return false, err
	}
	if b == start2 {
		return true, nil
	}
	if b == start1 {
		_ = br.UnreadByte()
	}
	return false, nil
}
