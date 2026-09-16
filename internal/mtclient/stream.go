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
		b, err := br.ReadByte()
		if err != nil {
			return err
		}
		if b != start1 {
			continue
		}
		b, err = br.ReadByte()
		if err != nil {
			return err
		}
		if b != start2 {
			if b == start1 {
				_ = br.UnreadByte()
			}
			continue
		}
		var lenBuf [2]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return err
		}
		n := int(binary.BigEndian.Uint16(lenBuf[:]))
		if n > MaxFrame {
			continue
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(br, payload); err != nil {
			return err
		}
		if !fn(payload) {
			return nil
		}
	}
}
