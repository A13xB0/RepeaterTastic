//go:build linux

package spi

import (
	"time"

	"golang.org/x/sys/unix"
)

// readRAKEEPROM reads a RAK board's autoconf string from the EEPROM at 0x50 on an I2C bus.
func readRAKEEPROM(dev string) (string, error) {
	fd, err := unix.Open(dev, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	const i2cSlave = 0x0703
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), i2cSlave, 0x50); errno != 0 {
		return "", errno
	}
	if _, err := unix.Write(fd, []byte{0x00, 0x00}); err != nil {
		return "", err
	}
	time.Sleep(10 * time.Millisecond)
	buf := make([]byte, 75)
	n, err := unix.Read(fd, buf)
	if err != nil {
		return "", err
	}
	return parseRAKEEPROM(buf[:n])
}
