//go:build linux

package spi

import (
	"errors"

	"golang.org/x/sys/unix"
)

// deviceGone reports whether an error means the adapter is no longer ours (a CH341 stick
// unplugged, re-enumerated, or reset, which drops our claim on its interface: EBUSY): the radio
// has to be opened again, which claims it anew.
func deviceGone(err error) bool {
	return errors.Is(err, unix.ENODEV) || errors.Is(err, unix.ESHUTDOWN) || errors.Is(err, unix.ENOENT) ||
		errors.Is(err, unix.EBUSY)
}
