package main

import (
	"encoding/binary"
	"io"
	"net"
	"sync"
)

// KISS framing and the MeshCore KISS modem command set, as seen from the modem side.
const (
	kFEND  = 0xC0
	kFESC  = 0xDB
	kTFEND = 0xDC
	kTFESC = 0xDD

	kTypeData = 0x00
	kTypeHW   = 0x06

	kSetRadio      = 0x09
	kSetTxPower    = 0x0A
	kGetRadio      = 0x0B
	kGetTxPower    = 0x0C
	kGetNoiseFloor = 0x10
	kGetVersion    = 0x11
	kGetStats      = 0x12
	kGetDeviceName = 0x16
	kPing          = 0x17
	kSetSyncWord   = 0x1B
	kSetPreamble   = 0x1C
	kGetPhyExtra   = 0x1D

	kRespOK     = 0xF0
	kRespError  = 0xF1
	kRespTxDone = 0xF8
	kRespRxMeta = 0xF9
)

// fakeModem is a minimal patched MeshCore KISS modem on the far end of a net.Pipe.
type fakeModem struct {
	version   byte
	name      string
	failRadio bool // answer GetRadio with an error
	txResult  byte // TxDone result byte (1 = ok)
	onDial    func()
	wmu       sync.Mutex
	mu        sync.Mutex
	conn      net.Conn
	sent      [][]byte
	freq, bw  uint32
	sf, cr    byte
	power     byte
	sync      byte
	preamble  uint16
}

func newFakeModem() *fakeModem {
	return &fakeModem{version: 2, name: "Heltec V3", txResult: 1, sync: 0x12}
}

func (f *fakeModem) dial() (io.ReadWriteCloser, error) {
	host, dev := net.Pipe()
	f.mu.Lock()
	f.conn = dev
	f.mu.Unlock()
	go f.serve(dev)
	if f.onDial != nil {
		f.onDial()
	}
	return host, nil
}

func (f *fakeModem) write(c net.Conn, typ byte, data []byte) {
	out := []byte{kFEND, typ}
	for _, b := range data {
		switch b {
		case kFEND:
			out = append(out, kFESC, kTFEND)
		case kFESC:
			out = append(out, kFESC, kTFESC)
		default:
			out = append(out, b)
		}
	}
	out = append(out, kFEND)
	f.wmu.Lock()
	defer f.wmu.Unlock()
	_, _ = c.Write(out)
}

// receive injects an over-the-air packet with its RSSI/SNR metadata.
func (f *fakeModem) receive(data []byte, snrQuarter, rssi int8) {
	f.mu.Lock()
	c := f.conn
	f.mu.Unlock()
	f.write(c, kTypeData, data)
	f.write(c, kTypeHW, []byte{kRespRxMeta, byte(snrQuarter), byte(rssi)})
}

func (f *fakeModem) serve(c net.Conn) {
	var frame []byte
	inFrame, esc := false, false
	buf := make([]byte, 256)
	for {
		n, err := c.Read(buf)
		for _, b := range buf[:n] {
			switch {
			case b == kFEND:
				if inFrame && len(frame) > 0 {
					f.handle(c, frame)
				}
				frame, inFrame = nil, true
			case b == kFESC:
				esc = true
			case esc:
				esc = false
				frame = append(frame, map[byte]byte{kTFEND: kFEND, kTFESC: kFESC}[b])
			default:
				frame = append(frame, b)
			}
		}
		if err != nil {
			return
		}
	}
}

func (f *fakeModem) handle(c net.Conn, fr []byte) {
	switch fr[0] {
	case kTypeData:
		f.mu.Lock()
		f.sent = append(f.sent, append([]byte(nil), fr[1:]...))
		res := f.txResult
		f.mu.Unlock()
		go f.write(c, kTypeHW, []byte{kRespTxDone, res})
	case kTypeHW:
		if len(fr) < 2 {
			return
		}
		reply := f.command(fr[1], fr[2:])
		go f.write(c, kTypeHW, reply)
	}
}

func (f *fakeModem) command(cmd byte, a []byte) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	u32 := binary.LittleEndian.AppendUint32
	ok := []byte{kRespOK}
	switch cmd {
	case kPing:
		return []byte{0x97}
	case kGetVersion:
		return []byte{0x91, f.version, 0}
	case kGetDeviceName:
		return append([]byte{0x96}, f.name...)
	case kSetRadio:
		f.freq, f.bw = binary.LittleEndian.Uint32(a), binary.LittleEndian.Uint32(a[4:])
		f.sf, f.cr = a[8], a[9]
		return ok
	case kGetRadio:
		if f.failRadio {
			return []byte{kRespError, 0x02}
		}
		return append(u32(u32([]byte{0x8B}, f.freq), f.bw), f.sf, f.cr)
	case kSetTxPower:
		f.power = a[0]
		return ok
	case kGetTxPower:
		return []byte{0x8C, f.power}
	case kSetSyncWord:
		f.sync = a[0]
		return ok
	case kSetPreamble:
		f.preamble = binary.LittleEndian.Uint16(a)
		return ok
	case kGetPhyExtra:
		return binary.LittleEndian.AppendUint16([]byte{0x9D, f.sync}, f.preamble)
	case kGetStats:
		return u32(u32(u32([]byte{0x92}, 7), uint32(len(f.sent))), 1)
	case kGetNoiseFloor:
		return binary.LittleEndian.AppendUint16([]byte{0x90}, uint16(0xFF92)) // -110 dBm
	}
	return []byte{kRespError, 0x05}
}

func (f *fakeModem) sentFrames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.sent...)
}
