//go:build !linux

package sx126x

import "errors"

func openHAL(Board, func(string, ...any)) (hal, error) {
	return nil, errors.New("sx126x: SPI radios need Linux (spidev and gpiochip)")
}
