//go:build linux

package spi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ch341HAL drives a CH341 USB-to-SPI adapter (MeshStick, PiNedio USB, uMesh, Meshtoad) through
// usbfs, without libusb. Pins are CH341 D0–D7; the IRQ line is polled, as libpinedio-usb does.
type ch341HAL struct {
	fd      int
	mu      sync.Mutex
	state   byte // D0–D5 output levels
	outputs byte // D0–D5 directions
	cs      int
	irq     Pin
	busy    Pin
	reset   Pin
	txen    Pin
	rxen    Pin
}

type usbdevfsBulk struct {
	ep, len, timeout uint32
	data             unsafe.Pointer
}

type usbdevfsIoctl struct {
	ifno, code int32
	data       unsafe.Pointer
}

var (
	usbdevfsBulkReq     = ioc(iocRW, 'U', 2, unsafe.Sizeof(usbdevfsBulk{}))
	usbdevfsClaimIface  = ioc(2, 'U', 15, 4) // _IOR
	usbdevfsReleaseIfc  = ioc(2, 'U', 16, 4)
	usbdevfsIoctlReq    = ioc(iocRW, 'U', 18, unsafe.Sizeof(usbdevfsIoctl{}))
	usbdevfsDisconnect  = ioc(0, 'U', 22, 0) // _IO
	usbdevfsConnect     = ioc(0, 'U', 23, 0)
	ch341DefaultOutputs = byte(1<<3 | 1<<5) // DCK and DOUT, as Ch341Hal sets them
)

func openCH341(b Board, logf func(string, ...any)) (hal, error) {
	path, product, err := findUSB(b.USB)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w (add a udev rule or run as root for USB access)", path, err)
	}
	h := &ch341HAL{fd: fd, outputs: ch341DefaultOutputs, cs: b.CS.Line, irq: b.IRQ, busy: b.Busy, reset: b.Reset, txen: b.TXen, rxen: b.RXen}
	// Detach a kernel driver if one claimed the interface (not usually for PID 5512).
	req := usbdevfsIoctl{ifno: 0, code: int32(usbdevfsDisconnect)}
	if err := ioctlPtr(fd, usbdevfsIoctlReq, unsafe.Pointer(&req)); err != nil && !errors.Is(err, unix.ENODATA) {
		logf("ch341: couldn't detach the kernel driver: %v", err)
	}
	iface := uint32(0)
	if err := ioctlPtr(fd, usbdevfsClaimIface, unsafe.Pointer(&iface)); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("claim CH341 interface: %w (is meshtasticd still running?)", err)
	}
	for _, p := range []Pin{b.CS, b.Reset, b.TXen, b.RXen} {
		if p.Set {
			if p.Line > 5 {
				h.Close()
				return nil, fmt.Errorf("ch341: D%d can't be an output (D0–D5 only)", p.Line)
			}
			h.outputs |= 1 << p.Line
		}
	}
	h.drain()
	h.state = 1 << h.cs // CS idle high
	if b.Reset.Set {
		h.state |= 1 << b.Reset.Line
	}
	if err := h.writePins(); err != nil {
		h.Close()
		return nil, err
	}
	logf("ch341: opened %s (%s)", path, product)
	return h, nil
}

// findUSB finds the adapter's /dev/bus/usb node through sysfs.
func findUSB(id *USBID) (path, product string, err error) {
	dirs, _ := filepath.Glob("/sys/bus/usb/devices/*")
	read := func(dir, name string) string {
		b, _ := os.ReadFile(filepath.Join(dir, name))
		return strings.TrimSpace(string(b))
	}
	for _, d := range dirs {
		vid, _ := strconv.ParseUint(read(d, "idVendor"), 16, 16)
		pid, _ := strconv.ParseUint(read(d, "idProduct"), 16, 16)
		if uint16(vid) != id.VID || uint16(pid) != id.PID {
			continue
		}
		if id.Serial != "" && !strings.HasPrefix(read(d, "serial"), id.Serial) {
			continue
		}
		bus, _ := strconv.Atoi(read(d, "busnum"))
		dev, _ := strconv.Atoi(read(d, "devnum"))
		return fmt.Sprintf("/dev/bus/usb/%03d/%03d", bus, dev), read(d, "product"), nil
	}
	want := fmt.Sprintf("%04x:%04x", id.VID, id.PID)
	if id.Serial != "" {
		want += " with serial " + id.Serial
	}
	return "", "", fmt.Errorf("no CH341 USB radio %s found (check lsusb)", want)
}

func (h *ch341HAL) bulk(ep uint32, data []byte) (int, error) {
	return h.bulkTimeout(ep, data, 1000)
}

func (h *ch341HAL) bulkTimeout(ep uint32, data []byte, ms uint32) (int, error) {
	req := usbdevfsBulk{ep: ep, len: uint32(len(data)), timeout: ms, data: unsafe.Pointer(&data[0])}
	n, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(h.fd), usbdevfsBulkReq, uintptr(unsafe.Pointer(&req)))
	if errno != 0 {
		return 0, fmt.Errorf("ch341 USB transfer: %w", errno)
	}
	return int(n), nil
}

// readIn reads one reply packet. The buffer is always a whole packet: a shorter one fails with
// EOVERFLOW when the adapter sends more than asked, as it does after an interrupted session.
func (h *ch341HAL) readIn(want int) ([]byte, error) {
	buf := make([]byte, ch341PacketLen)
	n, err := h.bulkTimeout(ch341EPIn, buf, 1000)
	if err != nil {
		return nil, err
	}
	if n > want {
		n = want
	}
	return buf[:n], nil
}

// drain throws away replies left over from a previous session that ended mid-transfer.
func (h *ch341HAL) drain() {
	buf := make([]byte, ch341PacketLen)
	for i := 0; i < 8; i++ {
		if n, err := h.bulkTimeout(ch341EPIn, buf, 50); err != nil || n == 0 {
			return
		}
	}
}

func (h *ch341HAL) writePins() error {
	_, err := h.bulk(ch341EPOut, ch341UIO(h.state, h.outputs))
	return err
}

func (h *ch341HAL) setPin(line int, v bool) error {
	if v {
		h.state |= 1 << line
	} else {
		h.state &^= 1 << line
	}
	return h.writePins()
}

func (h *ch341HAL) getPin(line int) (bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.bulk(ch341EPOut, []byte{ch341CmdGetInput}); err != nil {
		return false, err
	}
	r, err := h.readIn(6)
	if err != nil {
		return false, err
	}
	return ch341Inputs(r)&(1<<line) != 0, nil
}

// Transfer sends each 31-byte stream packet and reads its reply before the next, so the adapter's
// IN buffer never fills while the host is still writing.
func (h *ch341HAL) Transfer(tx []byte) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.setPin(h.cs, false); err != nil {
		return nil, err
	}
	defer h.setPin(h.cs, true)
	rx := make([]byte, 0, len(tx))
	for _, p := range ch341SPIPackets(tx) {
		if _, err := h.bulk(ch341EPOut, p); err != nil {
			return nil, err
		}
		for got := 0; got < len(p)-1; {
			r, err := h.readIn(len(p) - 1 - got)
			if err != nil {
				return nil, err
			}
			if len(r) == 0 {
				return nil, errors.New("ch341: short SPI read")
			}
			for _, b := range r {
				rx = append(rx, reverseBits(b))
			}
			got += len(r)
		}
	}
	return rx, nil
}

func (h *ch341HAL) Reset() error {
	if !h.reset.Set {
		return nil
	}
	h.mu.Lock()
	err := h.setPin(h.reset.Line, false)
	h.mu.Unlock()
	if err != nil {
		return err
	}
	time.Sleep(2 * time.Millisecond)
	h.mu.Lock()
	err = h.setPin(h.reset.Line, true)
	h.mu.Unlock()
	time.Sleep(10 * time.Millisecond)
	return err
}

func (h *ch341HAL) HasBusy() bool { return h.busy.Set }

func (h *ch341HAL) Busy() (bool, error) { return h.getPin(h.busy.Line) }

func (h *ch341HAL) HasIRQ() bool { return h.irq.Set }

// WaitIRQ polls the IRQ line at about 100 Hz.
func (h *ch341HAL) WaitIRQ(timeout time.Duration) (bool, error) {
	if !h.irq.Set {
		time.Sleep(timeout)
		return false, nil
	}
	deadline := time.Now().Add(timeout)
	for {
		high, err := h.getPin(h.irq.Line)
		if err != nil || high {
			return high, err
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *ch341HAL) SetRFSwitch(tx bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.txen.Set {
		if err := h.setPin(h.txen.Line, tx); err != nil {
			return err
		}
	}
	if h.rxen.Set {
		return h.setPin(h.rxen.Line, !tx)
	}
	return nil
}

func (h *ch341HAL) Close() error {
	if h.fd < 0 {
		return nil
	}
	iface := uint32(0)
	_ = ioctlPtr(h.fd, usbdevfsReleaseIfc, unsafe.Pointer(&iface))
	req := usbdevfsIoctl{ifno: 0, code: int32(usbdevfsConnect)}
	_ = ioctlPtr(h.fd, usbdevfsIoctlReq, unsafe.Pointer(&req))
	unix.Close(h.fd)
	h.fd = -1
	return nil
}
