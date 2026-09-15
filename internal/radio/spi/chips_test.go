package spi

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// fakeHAL is a hal whose transactions go to a chip model; raise fires the IRQ line.
type fakeHAL struct {
	mu      sync.Mutex
	xfer    func(tx []byte) []byte
	irqEdge chan struct{}
	busy    bool
}

func newFakeHAL(xfer func([]byte) []byte, busy bool) *fakeHAL {
	return &fakeHAL{xfer: xfer, irqEdge: make(chan struct{}, 8), busy: busy}
}

func (f *fakeHAL) Transfer(tx []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.xfer(tx), nil
}
func (f *fakeHAL) Reset() error              { return nil }
func (f *fakeHAL) HasBusy() bool             { return f.busy }
func (f *fakeHAL) Busy() (bool, error)       { return false, nil }
func (f *fakeHAL) HasIRQ() bool              { return true }
func (f *fakeHAL) SetRFSwitch(tx bool) error { return nil }
func (f *fakeHAL) Close() error              { return nil }
func (f *fakeHAL) WaitIRQ(timeout time.Duration) (bool, error) {
	select {
	case <-f.irqEdge:
		return true, nil
	case <-time.After(timeout):
		return false, nil
	}
}

// sendAndComplete sends a frame, raising TxDone once the chip model reports it is transmitting.
func sendAndComplete(t *testing.T, r *Radio, h *fakeHAL, transmitting func() bool, done func()) {
	t.Helper()
	go func() {
		for i := 0; i < 400; i++ {
			h.mu.Lock()
			tx := transmitting()
			if tx {
				done()
			}
			h.mu.Unlock()
			if tx {
				h.irqEdge <- struct{}{}
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.Send(ctx, []byte("hello mesh")); err != nil {
		t.Fatal(err)
	}
}

func waitFrame(t *testing.T, r *Radio) radio.Frame {
	t.Helper()
	select {
	case f := <-r.Frames():
		return f
	case <-time.After(2 * time.Second):
		t.Fatal("no frame")
	}
	return radio.Frame{}
}

// ------------------------------------------------------------------------------------ SX127x

type rf95Model struct {
	reg  [0x80]byte
	fifo [256]byte
}

func (m *rf95Model) xfer(tx []byte) []byte {
	rx := make([]byte, len(tx))
	addr := tx[0] & 0x7F
	write := tx[0]&0x80 != 0
	for i := 1; i < len(tx); i++ {
		a := addr
		if addr != rfRegFifo {
			a = addr + byte(i-1)
		}
		switch {
		case a == rfRegFifo && write:
			m.fifo[m.reg[rfRegFifoAddrPtr]] = tx[i]
			m.reg[rfRegFifoAddrPtr]++
		case a == rfRegFifo:
			rx[i] = m.fifo[m.reg[rfRegFifoAddrPtr]]
			m.reg[rfRegFifoAddrPtr]++
		case a == rfRegIrqFlags && write:
			m.reg[a] &^= tx[i] // write 1 to clear
		case a == rfRegOpMode && write:
			if m.reg[a]&7 != rfModeSleep && (tx[i]^m.reg[a])&rfLongRangeMode != 0 {
				tx[i] = tx[i]&^rfLongRangeMode | m.reg[a]&rfLongRangeMode // LoRa bit only changes in sleep
			}
			m.reg[a] = tx[i]
		case write:
			m.reg[a] = tx[i]
		default:
			rx[i] = m.reg[a]
		}
	}
	return rx
}

func TestSX127x(t *testing.T) {
	m := &rf95Model{}
	m.reg[rfRegVersion] = 0x12
	h := newFakeHAL(m.xfer, false)
	r, err := newRadio(context.Background(), h, Board{Module: ModuleRF95, MaxPower: 20}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := longFast
	c.TxPowerDBm = 18
	if err := r.Configure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	frf := uint32(m.reg[rfRegFrfMsb])<<16 | uint32(m.reg[rfRegFrfMsb+1])<<8 | uint32(m.reg[rfRegFrfMsb+2])
	if want := uint32(uint64(869_525_000) * (1 << 19) / 32_000_000); frf != want {
		t.Errorf("FRF 0x%06x, want 0x%06x", frf, want)
	}
	if m.reg[rfRegModemConfig1] != 0x80|1<<1 || m.reg[rfRegModemConfig2]>>4 != 11 || m.reg[rfRegModemConfig2]&0x04 == 0 {
		t.Errorf("modem config %02x %02x", m.reg[rfRegModemConfig1], m.reg[rfRegModemConfig2])
	}
	if m.reg[rfRegSyncWord] != 0x2B || m.reg[rfRegPreambleMsb+1] != 16 || m.reg[rfRegOpMode] != rfLongRangeMode|rfModeRxCont {
		t.Errorf("sync 0x%02x preamble %d mode 0x%02x", m.reg[rfRegSyncWord], m.reg[rfRegPreambleMsb+1], m.reg[rfRegOpMode])
	}
	if m.reg[rfRegPaConfig] != 0xF0|15 { // 18 dBm isn't settable on PA_BOOST: 17
		t.Errorf("PA config 0x%02x", m.reg[rfRegPaConfig])
	}
	// A packet: FIFO at 0x10, CRC present, RSSI raw 60 at 869 MHz = -97 dBm, SNR -2.5 dB.
	copy(m.fifo[0x10:], "abcdef")
	m.reg[rfRegFifoRxCurrent], m.reg[rfRegRxNbBytes] = 0x10, 6
	m.reg[rfRegHopChannel] = 0x40
	m.reg[rfRegPktSnr], m.reg[rfRegPktRssi] = byte(0xF6), 60
	m.reg[rfRegIrqFlags] = rfIrqRxDone | rfIrqValidHeader
	h.mu.Unlock()
	h.irqEdge <- struct{}{}
	f := waitFrame(t, r)
	if string(f.Data) != "abcdef" || f.RSSI != -157+60-2 || f.SNR != -2.5 {
		t.Fatalf("frame %q RSSI %d SNR %g", f.Data, f.RSSI, f.SNR)
	}
	sendAndComplete(t, r, h, func() bool { return m.reg[rfRegOpMode]&7 == rfModeTx },
		func() { m.reg[rfRegIrqFlags] |= rfIrqTxDone; m.reg[rfRegOpMode] = rfLongRangeMode | rfModeStandby })
	if !bytes.Equal(m.fifo[:10], []byte("hello mesh")) || m.reg[rfRegPayloadLength] != 10 {
		t.Fatalf("TX FIFO %q len %d", m.fifo[:10], m.reg[rfRegPayloadLength])
	}
}

// ------------------------------------------------------------------------------------ SX1280

type sx1280Model struct {
	regs  map[uint16]byte
	buf   [256]byte
	irq   uint16
	mode  byte
	cmds  map[byte][]byte
	rxLen byte
}

func (m *sx1280Model) xfer(tx []byte) []byte {
	rx := make([]byte, len(tx))
	m.cmds[tx[0]] = append([]byte(nil), tx[1:]...)
	switch tx[0] {
	case s8CmdReadRegister:
		a := uint16(tx[1])<<8 | uint16(tx[2])
		for i := 4; i < len(tx); i++ {
			rx[i] = m.regs[a+uint16(i-4)]
		}
	case s8CmdWriteRegister:
		a := uint16(tx[1])<<8 | uint16(tx[2])
		for i, v := range tx[3:] {
			m.regs[a+uint16(i)] = v
		}
	case s8CmdWriteBuffer:
		copy(m.buf[tx[1]:], tx[2:])
	case s8CmdReadBuffer:
		copy(rx[3:], m.buf[tx[1]:])
	case s8CmdGetIrqStatus:
		rx[2], rx[3] = byte(m.irq>>8), byte(m.irq)
	case s8CmdClearIrqStatus:
		m.irq &^= uint16(tx[1])<<8 | uint16(tx[2])
	case s8CmdGetRxBufferStatus:
		rx[2], rx[3] = m.rxLen, 0
	case s8CmdGetPacketStatus:
		rx[2], rx[3] = 150, 20 // -75 dBm, SNR 5
	case s8CmdSetStandby, s8CmdSetRx, s8CmdSetTx:
		m.mode = tx[0]
	}
	return rx
}

func TestSX1280(t *testing.T) {
	m := &sx1280Model{regs: map[uint16]byte{}, cmds: map[byte][]byte{}}
	for i, c := range []byte("SX1280 V3B") {
		m.regs[s8RegVersionString+uint16(i)] = c
	}
	h := newFakeHAL(m.xfer, true)
	r, err := newRadio(context.Background(), h, Board{Module: ModuleSX1280}, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := radio.Config{FrequencyHz: 2_403_000_000, BandwidthHz: 812_500, SF: 11, CR: 5, SyncWord: 0x2B, Preamble: 16, TxPowerDBm: 10}
	if err := r.Configure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	if got := m.cmds[s8CmdSetModulation]; !bytes.Equal(got, []byte{0xB0, 0x18, 1}) {
		t.Errorf("modulation %x", got)
	}
	if got := m.cmds[s8CmdSetPacketParams]; got[0] != 0x18 || got[3] != 0x20 || got[4] != 0x40 { // 16 = 8×2^1, RadioLib's search order
		t.Errorf("packet params %x", got)
	}
	if m.regs[s8RegLoRaSyncWordMsb] != 0x24 || m.regs[s8RegLoRaSyncWordMsb+1] != 0xB4 || m.cmds[s8CmdSetTxParams][0] != 28 {
		t.Errorf("sync %02x%02x power %d", m.regs[s8RegLoRaSyncWordMsb], m.regs[s8RegLoRaSyncWordMsb+1], m.cmds[s8CmdSetTxParams][0])
	}
	copy(m.buf[:], "2.4GHz")
	m.rxLen, m.irq = 6, s8IrqRxDone
	h.mu.Unlock()
	h.irqEdge <- struct{}{}
	if f := waitFrame(t, r); string(f.Data) != "2.4GHz" || f.RSSI != -75 || f.SNR != 5 {
		t.Fatalf("frame %q RSSI %d SNR %g", f.Data, f.RSSI, f.SNR)
	}
	sendAndComplete(t, r, h, func() bool { return m.mode == s8CmdSetTx }, func() { m.irq |= s8IrqTxDone; m.mode = s8CmdSetStandby })
	if m.mode != s8CmdSetRx {
		t.Fatal("not back in RX")
	}
}

// ------------------------------------------------------------------------------------ LR11x0

type lr1121Model struct {
	cmds    map[uint16][]byte
	pending []byte // response to the last read command
	irq     uint32
	buf     []byte
	mode    uint16
	rx      []byte
}

func (m *lr1121Model) xfer(tx []byte) []byte {
	rx := make([]byte, len(tx))
	if len(tx) == 6 && tx[0] == 0 && tx[1] == 0 && m.pending == nil { // IRQ status poll
		copy(rx[2:], be32(m.irq))
		return rx
	}
	if tx[0] == 0 && tx[1] == 0 { // second phase of a read
		copy(rx[1:], m.pending)
		m.pending = nil
		return rx
	}
	op := uint16(tx[0])<<8 | uint16(tx[1])
	args := append([]byte(nil), tx[2:]...)
	m.cmds[op] = args
	switch op {
	case lrCmdGetVersion:
		m.pending = []byte{0x22, lrDeviceLR1121, 0x01, 0x03}
	case lrCmdGetErrors:
		m.pending = []byte{0, 0}
	case lrCmdClearIrq:
		v := uint32(args[0])<<24 | uint32(args[1])<<16 | uint32(args[2])<<8 | uint32(args[3])
		m.irq &^= v
	case lrCmdWriteBuffer:
		m.buf = args
	case lrCmdGetRxBufferStatus:
		m.pending = []byte{byte(len(m.rx)), 0}
	case lrCmdReadBuffer:
		m.pending = m.rx
	case lrCmdGetPacketStatus:
		m.pending = []byte{180, 0xF8, 190} // -90 dBm, SNR -2
	case lrCmdSetRx, lrCmdSetTx, lrCmdSetStandby:
		m.mode = op
	}
	return rx
}

func TestLR1121(t *testing.T) {
	m := &lr1121Model{cmds: map[uint16][]byte{}}
	h := newFakeHAL(m.xfer, true)
	b, err := ParseBoard([]byte("Lora:\n  Module: lr1121\n  Busy: 4\n  DIO3_TCXO_VOLTAGE: 1.8\n"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := newRadio(context.Background(), h, b, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := m.cmds[lrCmdSetTcxoMode]; !bytes.Equal(got, []byte{2, 0, 0, 163}) {
		t.Errorf("TCXO %x", got)
	}
	c := longFast
	if err := r.Configure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	if got := m.cmds[lrCmdSetRfFrequency]; !bytes.Equal(got, be32(869_525_000)) {
		t.Errorf("frequency %x", got)
	}
	if got := m.cmds[lrCmdSetModulation]; !bytes.Equal(got, []byte{11, 0x05, 1, 0}) {
		t.Errorf("modulation %x", got)
	}
	if got := m.cmds[lrCmdSetLoRaSyncWord]; !bytes.Equal(got, []byte{0x2B}) {
		t.Errorf("sync word %x", got)
	}
	if got := m.cmds[lrCmdSetPaConfig]; got[0] != 0x01 || m.cmds[lrCmdSetTxParams][0] != 22 { // HP PA for 22 dBm
		t.Errorf("PA %x power %d", got, m.cmds[lrCmdSetTxParams][0])
	}
	if got := m.cmds[lrCmdCalibImage]; !bytes.Equal(got, []byte{216, 219}) {
		t.Errorf("image calibration %v", got)
	}
	m.rx, m.irq = []byte("lr1121"), lrIrqRxDone
	h.mu.Unlock()
	h.irqEdge <- struct{}{}
	if f := waitFrame(t, r); string(f.Data) != "lr1121" || f.RSSI != -90 || f.SNR != -2 {
		t.Fatalf("frame %q RSSI %d SNR %g", f.Data, f.RSSI, f.SNR)
	}
	sendAndComplete(t, r, h, func() bool { return m.mode == lrCmdSetTx }, func() { m.irq |= lrIrqTxDone; m.mode = lrCmdSetStandby })
	if string(m.buf) != "hello mesh" || m.mode != lrCmdSetRx {
		t.Fatalf("TX buffer %q, mode 0x%04x", m.buf, m.mode)
	}
}
