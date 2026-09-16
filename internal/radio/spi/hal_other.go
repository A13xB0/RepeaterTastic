//go:build !linux

package spi

import "errors"

func openHAL(Board, func(string, ...any)) (hal, error) {
	return nil, errors.New("spi: SPI radios need Linux (spidev and gpiochip)")
}

func openCH341(Board, func(string, ...any)) (hal, error) {
	return nil, errors.New("spi: CH341 USB radios need Linux (usbfs)")
}

func readRAKEEPROM(string) (string, error) {
	return "", errors.New("I2C needs Linux")
}
