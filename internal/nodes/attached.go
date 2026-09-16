// Package nodes connects RepeaterTastic to real Meshtastic nodes: boards running Meshtastic
// firmware (attached nodes) and meshtasticd instances it runs itself (hosted nodes).
package nodes

import (
	"context"
	"errors"
	"strings"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
)

// Driver is the radio.driver value for a node running Meshtastic firmware.
const Driver = "meshtastic"

// Attached is a board running Meshtastic firmware (over USB serial), or a meshtasticd on the
// network, standing in for a radio. The host it serves has one identity: the node itself. The
// node does all the radio work; Attached keeps the host's view of it current.
type Attached struct {
	*Node
	frames chan radio.Frame
	closed bool
}

var (
	_ radio.Radio        = (*Attached)(nil)
	_ mesh.ConfigApplier = (*Attached)(nil)
)

// OpenAttached starts connecting to the node at addr (a serial device or host[:port]). stateDir
// keeps the node's last state so the identity exists while the node is away.
func OpenAttached(ctx context.Context, addr, stateDir string, logf func(string, ...any)) (*Attached, error) {
	return OpenAttachedWith(ctx, mtclient.Options{Address: addr}, stateDir, logf)
}

// OpenAttachedWith is OpenAttached with full client options (tests dial a fake node).
func OpenAttachedWith(ctx context.Context, o mtclient.Options, stateDir string, logf func(string, ...any)) (*Attached, error) {
	addr := o.Address
	if strings.TrimSpace(addr) == "" {
		return nil, errors.New("meshtastic: no device: give a serial port or host[:port]")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	o.Logf = logf
	a := &Attached{Node: newNode(addr, stateDir, mtclient.New(o), logf, true), frames: make(chan radio.Frame)}
	if err := a.client.Start(ctx); err != nil {
		return nil, err
	}
	return a, nil
}

// ------------------------------------------------------------------------------ radio.Radio

// Configure accepts anything: the node owns its PHY, and settings reach it through ApplyConfig.
func (a *Attached) Configure(context.Context, radio.Config) error { return nil }

// Send is never used: the host hands packets to the node, not frames to a modem.
func (a *Attached) Send(context.Context, []byte) error { return radio.ErrUnsupported }

func (a *Attached) Frames() <-chan radio.Frame { return a.frames }

func (a *Attached) ChannelBusy(context.Context) (bool, error) { return false, nil }

func (a *Attached) Info() radio.Info {
	s := a.client.Snapshot()
	name := s.Self().GetUser().GetLongName()
	if name == "" {
		name = "Meshtastic node"
	}
	fw := s.Metadata.GetFirmwareVersion()
	if fw != "" {
		fw = "Meshtastic " + fw
	}
	return radio.Info{Driver: Driver, Device: a.addr, Firmware: fw, Name: name}
}

func (a *Attached) Stats(context.Context) radio.Stats {
	s := a.client.Snapshot()
	return radio.Stats{Connected: s.Connected, Reconnects: s.Reconnects}
}

func (a *Attached) Close() error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	err := a.client.Close()
	close(a.frames)
	return err
}
