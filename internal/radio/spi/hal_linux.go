//go:build linux

package spi

import (
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// linuxHAL talks to the chip through spidev and the GPIO v2 character device, without CGO.
type linuxHAL struct {
	spi                   int
	speed                 uint32
	cs, reset, txen, rxen *gpioLine
	busy, irq             *gpioLine
	high                  []*gpioLine
	log                   func(string, ...any)
}

func openHAL(b Board, logf func(string, ...any)) (hal, error) {
	dev := b.SPIDev
	if !strings.HasPrefix(dev, "/") {
		dev = filepath.Join("/dev", dev)
	}
	fd, err := unix.Open(dev, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (is SPI enabled, e.g. dtparam=spi=on, and is the user in the spi group?)", dev, err)
	}
	h := &linuxHAL{spi: fd, speed: b.SPISpeed, log: logf}
	fail := func(err error) (hal, error) {
		h.Close()
		return nil, err
	}
	mode := uint8(0)
	bits := uint8(8)
	if err := ioctlPtr(fd, spiIOCWrMode, unsafe.Pointer(&mode)); err != nil {
		return fail(fmt.Errorf("spidev mode: %w", err))
	}
	if err := ioctlPtr(fd, spiIOCWrBitsPerWord, unsafe.Pointer(&bits)); err != nil {
		return fail(fmt.Errorf("spidev bits: %w", err))
	}
	speed := b.SPISpeed
	if err := ioctlPtr(fd, spiIOCWrMaxSpeedHz, unsafe.Pointer(&speed)); err != nil {
		return fail(fmt.Errorf("spidev speed: %w", err))
	}

	out := func(p Pin, name string, initial bool) (*gpioLine, error) {
		if !p.Set {
			return nil, nil
		}
		l, err := requestLine(p, gpioFlagOutput, "repeatertastic-"+name, initial)
		if err != nil {
			return nil, fmt.Errorf("%s (%s): %w", name, p, err)
		}
		return l, nil
	}
	for _, p := range b.High {
		l, err := out(p, "enable", true)
		if err != nil {
			return fail(err)
		}
		h.high = append(h.high, l)
	}
	if len(b.High) > 0 {
		time.Sleep(50 * time.Millisecond) // let a switched radio power up before reset
	}
	if b.CS.Set {
		cs, err := out(b.CS, "cs", true)
		if errors.Is(err, unix.EBUSY) {
			// The SPI controller already owns this pin as its chip select: let spidev drive it.
			logf("CS %s is owned by the SPI driver; using the spidev's own chip select", b.CS)
		} else if err != nil {
			return fail(err)
		}
		h.cs = cs
	}
	if h.reset, err = out(b.Reset, "reset", true); err != nil {
		return fail(err)
	}
	if h.txen, err = out(b.TXen, "txen", false); err != nil {
		return fail(err)
	}
	if h.rxen, err = out(b.RXen, "rxen", false); err != nil {
		return fail(err)
	}
	if b.Busy.Set {
		if h.busy, err = requestLine(b.Busy, gpioFlagInput, "repeatertastic-busy", false); err != nil {
			return fail(fmt.Errorf("busy (%s): %w", b.Busy, err))
		}
	}
	if b.IRQ.Set {
		if h.irq, err = requestLine(b.IRQ, gpioFlagInput|gpioFlagEdgeRising, "repeatertastic-irq", false); err != nil {
			return fail(fmt.Errorf("irq (%s): %w", b.IRQ, err))
		}
	}
	return h, nil
}

func (h *linuxHAL) Transfer(tx []byte) ([]byte, error) {
	rx := make([]byte, len(tx))
	buf := make([]byte, len(tx))
	copy(buf, tx)
	if h.cs != nil {
		if err := h.cs.Set(false); err != nil {
			return nil, err
		}
		defer h.cs.Set(true)
	}
	var xfer [32]byte
	binary.LittleEndian.PutUint64(xfer[0:], uint64(uintptr(unsafe.Pointer(&buf[0]))))
	binary.LittleEndian.PutUint64(xfer[8:], uint64(uintptr(unsafe.Pointer(&rx[0]))))
	binary.LittleEndian.PutUint32(xfer[16:], uint32(len(tx)))
	binary.LittleEndian.PutUint32(xfer[20:], h.speed)
	xfer[26] = 8 // bits_per_word
	err := ioctlPtr(h.spi, spiIOCMessage1, unsafe.Pointer(&xfer[0]))
	runtime.KeepAlive(buf)
	runtime.KeepAlive(rx)
	if err != nil {
		return nil, fmt.Errorf("spi transfer: %w", err)
	}
	return rx, nil
}

func (h *linuxHAL) Reset() error {
	if h.reset == nil {
		return nil
	}
	if err := h.reset.Set(false); err != nil {
		return err
	}
	time.Sleep(2 * time.Millisecond)
	if err := h.reset.Set(true); err != nil {
		return err
	}
	time.Sleep(10 * time.Millisecond)
	return nil
}

func (h *linuxHAL) HasBusy() bool { return h.busy != nil }

func (h *linuxHAL) Busy() (bool, error) { return h.busy.Get() }

func (h *linuxHAL) HasIRQ() bool { return h.irq != nil }

func (h *linuxHAL) WaitIRQ(timeout time.Duration) (bool, error) {
	if h.irq == nil {
		time.Sleep(timeout)
		return false, nil
	}
	return h.irq.WaitEdge(timeout)
}

func (h *linuxHAL) SetRFSwitch(tx bool) error {
	if h.txen != nil {
		if err := h.txen.Set(tx); err != nil {
			return err
		}
	}
	if h.rxen != nil {
		if err := h.rxen.Set(!tx); err != nil {
			return err
		}
	}
	return nil
}

func (h *linuxHAL) Close() error {
	for _, l := range append([]*gpioLine{h.cs, h.reset, h.txen, h.rxen, h.busy, h.irq}, h.high...) {
		if l != nil {
			l.Close()
		}
	}
	if h.spi > 0 {
		unix.Close(h.spi)
		h.spi = -1
	}
	return nil
}

// ------------------------------------------------------------------------------- ioctl numbers

// Linux generic _IOC encoding (arm, arm64, amd64): dir<<30 | size<<16 | type<<8 | nr.
func ioc(dir, typ, nr, size uintptr) uintptr { return dir<<30 | size<<16 | typ<<8 | nr }

const (
	iocWrite = 1
	iocRW    = 3
)

var (
	spiIOCWrMode        = ioc(iocWrite, 'k', 1, 1)
	spiIOCWrBitsPerWord = ioc(iocWrite, 'k', 3, 1)
	spiIOCWrMaxSpeedHz  = ioc(iocWrite, 'k', 4, 4)
	spiIOCMessage1      = ioc(iocWrite, 'k', 0, 32) // SPI_IOC_MESSAGE(1)

	gpioV2GetLine   = ioc(iocRW, 0xB4, 0x07, 592) // GPIO_V2_GET_LINE_IOCTL
	gpioV2GetValues = ioc(iocRW, 0xB4, 0x0E, 16)
	gpioV2SetValues = ioc(iocRW, 0xB4, 0x0F, 16)
)

func ioctlPtr(fd int, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// ------------------------------------------------------------------------------- GPIO v2

const (
	gpioFlagInput      = 1 << 2
	gpioFlagOutput     = 1 << 3
	gpioFlagEdgeRising = 1 << 4
)

type gpioLine struct {
	fd int
}

// requestLine asks /dev/gpiochipN for one line (struct gpio_v2_line_request, 592 bytes).
func requestLine(p Pin, flags uint64, consumer string, initial bool) (*gpioLine, error) {
	chip, err := unix.Open(fmt.Sprintf("/dev/gpiochip%d", p.Chip), unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(chip)
	var req [592]byte
	binary.LittleEndian.PutUint32(req[0:], uint32(p.Line)) // offsets[0]
	copy(req[256:287], consumer)                           // consumer[32]
	binary.LittleEndian.PutUint64(req[288:], flags)        // config.flags
	if flags&gpioFlagOutput != 0 {
		binary.LittleEndian.PutUint32(req[296:], 1) // config.num_attrs
		binary.LittleEndian.PutUint32(req[320:], 2) // attrs[0].attr.id = GPIO_V2_LINE_ATTR_ID_OUTPUT_VALUES
		if initial {
			binary.LittleEndian.PutUint64(req[328:], 1) // attrs[0].attr.values
		}
		binary.LittleEndian.PutUint64(req[336:], 1) // attrs[0].mask
	}
	binary.LittleEndian.PutUint32(req[560:], 1) // num_lines
	if flags&gpioFlagEdgeRising != 0 {
		binary.LittleEndian.PutUint32(req[564:], 16) // event_buffer_size
	}
	if err := ioctlPtr(chip, gpioV2GetLine, unsafe.Pointer(&req[0])); err != nil {
		return nil, err
	}
	return &gpioLine{fd: int(int32(binary.LittleEndian.Uint32(req[588:])))}, nil
}

func (l *gpioLine) Set(v bool) error {
	var vals [16]byte
	if v {
		binary.LittleEndian.PutUint64(vals[0:], 1)
	}
	binary.LittleEndian.PutUint64(vals[8:], 1)
	return ioctlPtr(l.fd, gpioV2SetValues, unsafe.Pointer(&vals[0]))
}

func (l *gpioLine) Get() (bool, error) {
	var vals [16]byte
	binary.LittleEndian.PutUint64(vals[8:], 1)
	if err := ioctlPtr(l.fd, gpioV2GetValues, unsafe.Pointer(&vals[0])); err != nil {
		return false, err
	}
	return binary.LittleEndian.Uint64(vals[0:])&1 == 1, nil
}

// WaitEdge waits for a rising edge and drains the queued events.
func (l *gpioLine) WaitEdge(timeout time.Duration) (bool, error) {
	fds := []unix.PollFd{{Fd: int32(l.fd), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(timeout.Milliseconds()))
	if err != nil {
		if errors.Is(err, unix.EINTR) {
			return false, nil
		}
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	var ev [48 * 16]byte // struct gpio_v2_line_event is 48 bytes
	_, _ = unix.Read(l.fd, ev[:])
	return true, nil
}

func (l *gpioLine) Close() {
	if l.fd > 0 {
		unix.Close(l.fd)
		l.fd = -1
	}
}
