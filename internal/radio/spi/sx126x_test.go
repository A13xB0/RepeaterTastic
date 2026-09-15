package spi

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// fakeChip answers SX126x SPI commands well enough to drive the driver: registers, the data
// buffer, IRQ flags and the operating mode.
type fakeChip struct {
	mu       sync.Mutex
	regs     map[uint16]byte
	buf      [256]byte
	irq      uint16
	irqEdge  chan struct{}
	log      [][]byte
	mode     byte // last of SetStandby/SetRx/SetTx
	rxLen    byte
	pktRSSI  byte
	pktSNR   int8
	txSwitch bool
	answer   bool // false = a chip that isn't there (all zeros)
}

func newFakeChip() *fakeChip {
	return &fakeChip{regs: map[uint16]byte{}, irqEdge: make(chan struct{}, 8), answer: true}
}

func (c *fakeChip) Transfer(tx []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.log = append(c.log, append([]byte(nil), tx...))
	rx := make([]byte, len(tx))
	if !c.answer {
		return rx, nil
	}
	switch tx[0] {
	case cmdReadRegister:
		addr := uint16(tx[1])<<8 | uint16(tx[2])
		for i := 4; i < len(tx); i++ {
			rx[i] = c.regs[addr+uint16(i-4)]
		}
	case cmdWriteRegister:
		addr := uint16(tx[1])<<8 | uint16(tx[2])
		for i, v := range tx[3:] {
			c.regs[addr+uint16(i)] = v
		}
	case cmdWriteBuffer:
		copy(c.buf[tx[1]:], tx[2:])
	case cmdReadBuffer:
		copy(rx[3:], c.buf[tx[1]:])
	case cmdGetIrqStatus:
		rx[2], rx[3] = byte(c.irq>>8), byte(c.irq)
	case cmdClearIrqStatus:
		c.irq &^= uint16(tx[1])<<8 | uint16(tx[2])
	case cmdGetRxBufferStatus:
		rx[2], rx[3] = c.rxLen, 0
	case cmdGetPacketStatus:
		rx[2], rx[3], rx[4] = c.pktRSSI, byte(c.pktSNR), 0
	case cmdGetRssiInst:
		rx[2] = 220 // -110 dBm
	case cmdSetStandby, cmdSetRx, cmdSetTx:
		c.mode = tx[0]
	}
	return rx, nil
}

func (c *fakeChip) Reset() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.regs[regSyncWord], c.regs[regSyncWord+1] = loraSyncWordReset1, loraSyncWordReset2
	return nil
}

func (c *fakeChip) Busy() (bool, error) { return false, nil }
func (c *fakeChip) HasIRQ() bool        { return true }
func (c *fakeChip) HasBusy() bool       { return true }

func (c *fakeChip) WaitIRQ(timeout time.Duration) (bool, error) {
	select {
	case <-c.irqEdge:
		return true, nil
	case <-time.After(timeout):
		return false, nil
	}
}

func (c *fakeChip) SetRFSwitch(tx bool) error {
	c.mu.Lock()
	c.txSwitch = tx
	c.mu.Unlock()
	return nil
}

func (c *fakeChip) Close() error { return nil }

func (c *fakeChip) raise(flags uint16) {
	c.mu.Lock()
	c.irq |= flags
	c.mu.Unlock()
	c.irqEdge <- struct{}{}
}

// sent returns the commands with opcode op, in order.
func (c *fakeChip) sent(op byte) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out [][]byte
	for _, t := range c.log {
		if t[0] == op {
			out = append(out, t)
		}
	}
	return out
}

var longFast = radio.Config{FrequencyHz: 869_525_000, BandwidthHz: 250_000, SF: 11, CR: 5, SyncWord: 0x2B, Preamble: 16, TxPowerDBm: 27}

func openFake(t *testing.T, b Board) (*Radio, *fakeChip) {
	t.Helper()
	chip := newFakeChip()
	if b.Module == "" {
		b.Module = ModuleSX1262
	}
	r, err := newRadio(context.Background(), chip, b, t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r, chip
}

func TestOpenChecksTheChipAnswers(t *testing.T) {
	chip := newFakeChip()
	chip.answer = false
	if _, err := newRadio(context.Background(), chip, Board{Module: ModuleSX1262}, t.Logf); err == nil {
		t.Fatal("opened a chip that reads all zeros")
	}
}

func TestInitTCXOAndRFSwitch(t *testing.T) {
	_, chip := openFake(t, Board{TCXOVolt: 1.8, DIO2RF: true})
	if got := chip.sent(cmdSetDIO3AsTcxoCtrl); len(got) != 1 || got[0][1] != 2 {
		t.Fatalf("TCXO command %x, want voltage code 2 (1.8 V)", got)
	}
	if len(chip.sent(cmdCalibrate)) != 1 || len(chip.sent(cmdSetDIO2AsRfSwitch)) != 1 {
		t.Fatal("no calibrate or DIO2 RF switch")
	}
}

func TestConfigureLongFast(t *testing.T) {
	r, chip := openFake(t, Board{MaxPower: 22})
	if err := r.Configure(context.Background(), longFast); err != nil {
		t.Fatal(err)
	}
	// 869.525 MHz × 2^25 / 32 MHz = 0x36588000... check the arithmetic, not a pasted constant.
	frf := uint32(uint64(869_525_000) * (1 << 25) / 32_000_000)
	if got := chip.sent(cmdSetRfFrequency); len(got) != 1 || !bytes.Equal(got[0][1:], []byte{byte(frf >> 24), byte(frf >> 16), byte(frf >> 8), byte(frf)}) {
		t.Fatalf("frequency %x", got)
	}
	if got := chip.sent(cmdCalibrateImage); len(got) != 1 || got[0][1] != 0xD7 || got[0][2] != 0xDB {
		t.Fatalf("image calibration %x, want 863-870 band", got)
	}
	// SF11 at 250 kHz: 8.2 ms symbols, no LDRO.
	if got := chip.sent(cmdSetModulationParams); !bytes.Equal(got[0][1:], []byte{11, 0x05, 1, 0}) {
		t.Fatalf("modulation %x", got[0])
	}
	if chip.regs[regSyncWord] != 0x24 || chip.regs[regSyncWord+1] != 0xB4 {
		t.Fatalf("sync word %02x%02x, want 24b4", chip.regs[regSyncWord], chip.regs[regSyncWord+1])
	}
	if got := chip.sent(cmdSetTxParams); got[0][1] != 22 {
		t.Fatalf("tx power %d, want clamped to 22", got[0][1])
	}
	if chip.regs[regTxModulation]&0x04 == 0 || chip.regs[regIQPolarity]&0x04 == 0 || chip.regs[regTxClampConfig]&0x1E != 0x1E {
		t.Fatal("errata registers not set")
	}
	if chip.mode != cmdSetRx {
		t.Fatalf("mode 0x%02x after configure, want RX", chip.mode)
	}
}

func TestConfigureLDROAndLimits(t *testing.T) {
	r, chip := openFake(t, Board{MaxPower: 14})
	c := longFast
	c.BandwidthHz, c.SF, c.TxPowerDBm = 125_000, 12, 20 // VeryLongSlow: 32.8 ms symbols
	if err := r.Configure(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := chip.sent(cmdSetModulationParams); got[0][4] != 1 {
		t.Fatalf("LDRO off for SF12/125k: %x", got[0])
	}
	if got := chip.sent(cmdSetTxParams); got[0][1] != 14 {
		t.Fatalf("tx power %d, want the board's max 14", got[0][1])
	}
	c.BandwidthHz = 200_000
	if err := r.Configure(context.Background(), c); !errors.Is(err, radio.ErrUnsupported) {
		t.Fatalf("200 kHz: %v", err)
	}
}

func TestReceive(t *testing.T) {
	r, chip := openFake(t, Board{})
	if err := r.Configure(context.Background(), longFast); err != nil {
		t.Fatal(err)
	}
	payload := []byte{0xFF, 0xFF, 0xFF, 0xFF, 1, 2, 3, 4}
	chip.mu.Lock()
	copy(chip.buf[:], payload)
	chip.rxLen, chip.pktRSSI, chip.pktSNR = byte(len(payload)), 180, -22
	chip.mu.Unlock()
	chip.raise(irqRxDone)
	select {
	case f := <-r.Frames():
		if !bytes.Equal(f.Data, payload) || f.RSSI != -90 || f.SNR != -5.5 {
			t.Fatalf("frame %x RSSI %d SNR %g", f.Data, f.RSSI, f.SNR)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no frame")
	}
	chip.raise(irqRxDone | irqCrcErr)
	time.Sleep(50 * time.Millisecond)
	if st := r.Stats(context.Background()); st.RxPackets != 1 || st.Errors != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestChannelBusyOnPreamble(t *testing.T) {
	r, chip := openFake(t, Board{})
	if err := r.Configure(context.Background(), longFast); err != nil {
		t.Fatal(err)
	}
	chip.raise(irqPreamble)
	deadline := time.Now().Add(time.Second)
	for {
		if busy, _ := r.ChannelBusy(context.Background()); busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("not busy after a preamble")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestSend(t *testing.T) {
	r, chip := openFake(t, Board{})
	frame := []byte("hello mesh")
	if err := r.Send(context.Background(), frame); !errors.Is(err, radio.ErrNotConnected) {
		t.Fatalf("send before configure: %v", err)
	}
	if err := r.Configure(context.Background(), longFast); err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 200; i++ {
			chip.mu.Lock()
			tx := chip.mode == cmdSetTx
			chip.mu.Unlock()
			if tx {
				chip.raise(irqTxDone)
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := r.Send(ctx, frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(chip.buf[:len(frame)], frame) {
		t.Fatalf("buffer %q", chip.buf[:len(frame)])
	}
	pp := chip.sent(cmdSetPacketParams)
	if !slicesContainLen(pp, byte(len(frame))) {
		t.Fatalf("no packet params with length %d: %x", len(frame), pp)
	}
	if chip.mode != cmdSetRx || chip.txSwitch {
		t.Fatal("not back to RX after TxDone")
	}
	if st := r.Stats(context.Background()); st.TxPackets != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func slicesContainLen(cmds [][]byte, n byte) bool {
	for _, c := range cmds {
		if c[4] == n {
			return true
		}
	}
	return false
}

func TestTables(t *testing.T) {
	if tcxoCode(3.3) != 7 || tcxoCode(1.6) != 0 || tcxoCode(1.8) != 2 {
		t.Fatal("tcxo codes")
	}
	if b := imageBand(915_000_000); b != [2]byte{0xE1, 0xE9} {
		t.Fatalf("915 MHz band %x", b)
	}
	if b := imageBand(433_875_000); b != [2]byte{0x6B, 0x6F} {
		t.Fatalf("433 MHz band %x", b)
	}
}
