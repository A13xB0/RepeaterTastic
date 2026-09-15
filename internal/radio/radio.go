// Package radio abstracts a dumb LoRa modem: raw frames in and out, no Meshtastic knowledge.
package radio

import (
	"context"
	"errors"
	"time"
)

// Config is the PHY a modem is tuned to.
type Config struct {
	FrequencyHz uint32
	BandwidthHz uint32
	SF          uint8
	CR          uint8 // coding rate denominator, 5..8
	SyncWord    uint8
	Preamble    uint16
	TxPowerDBm  int8
}

// Frame is one received LoRa packet.
type Frame struct {
	Data []byte
	RSSI int16   // dBm
	SNR  float32 // dB
	At   time.Time
}

// Info describes the modem for status displays.
type Info struct {
	Driver   string // "kiss", "sim", ...
	Device   string // e.g. /dev/ttyUSB0
	Firmware string
	Name     string
}

// Stats are cumulative counters where the modem provides them.
type Stats struct {
	RxPackets, TxPackets, Errors uint32
	NoiseFloorDBm                int16
	Connected                    bool
	Reconnects                   int
}

var (
	ErrBusy         = errors.New("radio: transmitter busy")
	ErrTxFailed     = errors.New("radio: transmission failed")
	ErrNotConnected = errors.New("radio: not connected")
	ErrUnsupported  = errors.New("radio: modem does not support this setting")
)

// Radio is implemented by every modem driver. Implementations must be safe for concurrent use.
type Radio interface {
	// Configure applies PHY settings; returns ErrUnsupported if the modem can't set the sync word
	// or preamble (e.g. unpatched MeshCore KISS firmware).
	Configure(ctx context.Context, c Config) error
	// Send transmits one frame and returns once the modem reports TX done (or ctx ends).
	Send(ctx context.Context, frame []byte) error
	// Frames delivers received packets. The channel is closed when the radio is closed.
	Frames() <-chan Frame
	// ChannelBusy reports whether a LoRa transmission is currently being received (listen-before-talk).
	ChannelBusy(ctx context.Context) (bool, error)
	Info() Info
	Stats(ctx context.Context) Stats
	Close() error
}
