package kiss

import (
	"io"

	"go.bug.st/serial"
)

// dialSerial opens the modem's serial port 8N1. DTR/RTS keep the library default (asserted):
// native-USB boards need DTR to consider the host connected, and on CP210x boards with the
// usual auto-reset transistors both-asserted does not hold the MCU in reset.
func dialSerial(dev string, baud int) (io.ReadWriteCloser, error) {
	return serial.Open(dev, &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   serial.NoParity,
		StopBits: serial.OneStopBit,
	})
}
