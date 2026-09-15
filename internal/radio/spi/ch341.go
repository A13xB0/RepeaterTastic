package spi

// CH341 USB-to-SPI protocol, as libpinedio-usb (and flashrom's ch341a_spi) speak it: 32-byte
// bulk packets; SPI bytes go bit-reversed after a 0xA8 stream command, and D0–D5 outputs are set
// with a 0xAB UIO stream. This file is the portable encoding; ch341_linux.go does the USB I/O.

const (
	ch341PacketLen    = 32
	ch341CmdSPIStream = 0xA8
	ch341CmdGetInput  = 0xA0
	ch341CmdUIOStream = 0xAB
	ch341UIOOut       = 0x80
	ch341UIODir       = 0x40
	ch341UIOEnd       = 0x20
	ch341EPOut        = 0x02
	ch341EPIn         = 0x82
)

func reverseBits(b byte) byte {
	b = (b>>1)&0x55 | (b<<1)&0xAA
	b = (b>>2)&0x33 | (b<<2)&0xCC
	return (b>>4)&0x0F | (b<<4)&0xF0
}

// ch341SPIPackets splits an SPI transaction into stream packets of at most 31 data bytes.
func ch341SPIPackets(tx []byte) [][]byte {
	var out [][]byte
	for len(tx) > 0 {
		n := min(len(tx), ch341PacketLen-1)
		p := make([]byte, 1+n)
		p[0] = ch341CmdSPIStream
		for i, b := range tx[:n] {
			p[1+i] = reverseBits(b)
		}
		out = append(out, p)
		tx = tx[n:]
	}
	return out
}

// ch341UIO sets the D0–D5 output levels and directions.
func ch341UIO(state, outputs byte) []byte {
	return []byte{ch341CmdUIOStream, ch341UIOOut | state&0x3F, ch341UIODir | outputs&0x3F, ch341UIOEnd}
}

// ch341Inputs decodes a GetInput reply to a pin bitmap, as libpinedio-usb's pinedio_get_input:
// D0–D7 in the low byte, then the status lines (INT# is bit 10).
func ch341Inputs(r []byte) uint32 {
	if len(r) < 3 {
		return 0
	}
	return uint32(r[0]) | uint32(r[1]&0xEF)<<8 | uint32(r[2]&0x80)<<16
}
