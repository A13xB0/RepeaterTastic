package kiss

// KISS framing (KA9Q/K3MC) and the MeshCore KISS modem command set.
// See firmware/README.md and MeshCore docs/kiss_modem_protocol.md.

const (
	fend  = 0xC0
	fesc  = 0xDB
	tfend = 0xDC
	tfesc = 0xDD

	maxFrame  = 512 // unescaped type byte + data, as the firmware's rx buffer
	MaxPacket = 255 // Data frame payload limit (MeshCore MAX_TRANS_UNIT)
)

// KISS type bytes (port 0).
const (
	typeData        = 0x00
	typeTxDelay     = 0x01
	typePersistence = 0x02
	typeSetHardware = 0x06
	typeReturn      = 0xFF
)

// SetHardware sub-commands.
const (
	cmdSetRadio      = 0x09
	cmdSetTxPower    = 0x0A
	cmdGetRadio      = 0x0B
	cmdGetTxPower    = 0x0C
	cmdIsChannelBusy = 0x0E
	cmdGetAirtime    = 0x0F
	cmdGetNoiseFloor = 0x10
	cmdGetVersion    = 0x11
	cmdGetStats      = 0x12
	cmdGetDeviceName = 0x16
	cmdPing          = 0x17
	cmdSetSyncWord   = 0x1B // patched firmware (version 2)
	cmdSetPreamble   = 0x1C
	cmdGetPhyExtra   = 0x1D

	respOK     = 0xF0
	respError  = 0xF1
	respTxDone = 0xF8
	respRxMeta = 0xF9
)

// Error codes carried by respError.
const (
	ErrCodeInvalidLength = 0x01
	ErrCodeInvalidParam  = 0x02
	ErrCodeNoCallback    = 0x03
	ErrCodeUnknownCmd    = 0x05
	ErrCodeTxBusy        = 0x07 // radio TX pending, or modem's host output queue full
)

// appendFrame appends FEND type data... FEND with escaping.
func appendFrame(dst []byte, typ byte, parts ...[]byte) []byte {
	dst = append(dst, fend)
	dst = appendEscaped(dst, typ)
	for _, p := range parts {
		for _, b := range p {
			dst = appendEscaped(dst, b)
		}
	}
	return append(dst, fend)
}

func appendEscaped(dst []byte, b byte) []byte {
	switch b {
	case fend:
		return append(dst, fesc, tfend)
	case fesc:
		return append(dst, fesc, tfesc)
	}
	return append(dst, b)
}

// decoder is an incremental KISS deframer mirroring the firmware's parser: bytes before the first
// FEND are ignored, empty frames skipped, invalid escapes dropped, oversize frames discarded.
type decoder struct {
	buf     []byte
	active  bool
	escaped bool
}

// feed consumes one byte and returns a complete frame (type byte first) when one ends.
// The returned slice is only valid until the next call.
func (d *decoder) feed(b byte) []byte {
	if b == fend {
		var out []byte
		if d.active && len(d.buf) > 0 {
			out = d.buf
		}
		d.buf = d.buf[:0]
		d.active, d.escaped = true, false
		return out
	}
	if !d.active {
		return nil
	}
	if b == fesc {
		d.escaped = true
		return nil
	}
	if d.escaped {
		d.escaped = false
		switch b {
		case tfend:
			b = fend
		case tfesc:
			b = fesc
		default:
			return nil
		}
	}
	if len(d.buf) >= maxFrame {
		d.buf = d.buf[:0]
		d.active = false
		return nil
	}
	d.buf = append(d.buf, b)
	return nil
}
