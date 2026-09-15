// Package wire holds Meshtastic's on-air packet format and cryptography.
//
// References are to meshtastic/firmware src/mesh at 2.8.1 (RadioInterface.h, CryptoEngine.cpp, Channels.cpp).
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/A13xB0/RepeaterTastic/pb"
)

const (
	HeaderLen    = 16
	MaxFrame     = 255
	MaxPayload   = MaxFrame - HeaderLen
	PKIOverhead  = 12
	AEADOverhead = 12

	Broadcast       uint32 = 0xFFFFFFFF
	BroadcastNoLoRa uint32 = 1
	NumReserved     uint32 = 4
	HopMax                 = 7
	HopReliable            = 3

	flagHopLimitMask = 0x07
	flagWantAck      = 0x08
	flagViaMQTT      = 0x10
	flagHopStartShft = 5
)

var ErrNotEncrypted = errors.New("packet must be encrypted before transmission")

// EncodeFrame serialises an encrypted MeshPacket into a LoRa frame.
func EncodeFrame(p *pb.MeshPacket) ([]byte, error) {
	enc, ok := p.PayloadVariant.(*pb.MeshPacket_Encrypted)
	if !ok {
		return nil, ErrNotEncrypted
	}
	if p.From == 0 {
		return nil, errors.New("packet has no sender")
	}
	if len(enc.Encrypted) > MaxPayload {
		return nil, fmt.Errorf("payload %d bytes exceeds %d", len(enc.Encrypted), MaxPayload)
	}
	hl := p.HopLimit
	if hl > HopMax {
		hl = HopReliable
	}
	flags := byte(hl & flagHopLimitMask)
	if p.WantAck {
		flags |= flagWantAck
	}
	if p.ViaMqtt {
		flags |= flagViaMQTT
	}
	flags |= byte(p.HopStart<<flagHopStartShft) & 0xE0
	f := make([]byte, HeaderLen, HeaderLen+len(enc.Encrypted))
	binary.LittleEndian.PutUint32(f[0:], p.To)
	binary.LittleEndian.PutUint32(f[4:], p.From)
	binary.LittleEndian.PutUint32(f[8:], p.Id)
	f[12] = flags
	f[13] = byte(p.Channel)
	f[14] = byte(p.NextHop)
	f[15] = byte(p.RelayNode)
	return append(f, enc.Encrypted...), nil
}

// DecodeFrame parses a LoRa frame; nil for frames the firmware drops.
func DecodeFrame(f []byte, rssi int32, snr float32) *pb.MeshPacket {
	if len(f) <= HeaderLen || len(f) > MaxFrame {
		return nil
	}
	p := &pb.MeshPacket{
		To:   binary.LittleEndian.Uint32(f[0:]),
		From: binary.LittleEndian.Uint32(f[4:]),
		Id:   binary.LittleEndian.Uint32(f[8:]),
	}
	if p.From == 0 {
		return nil
	}
	flags := f[12]
	p.Channel = uint32(f[13])
	p.HopLimit = uint32(flags & flagHopLimitMask)
	p.HopStart = uint32(flags&0xE0) >> flagHopStartShft
	p.WantAck = flags&flagWantAck != 0
	p.ViaMqtt = flags&flagViaMQTT != 0
	if p.HopStart != 0 {
		p.NextHop = uint32(f[14])
		p.RelayNode = uint32(f[15])
	}
	p.PayloadVariant = &pb.MeshPacket_Encrypted{Encrypted: append([]byte(nil), f[HeaderLen:]...)}
	if rssi != 0 {
		p.RxRssi = &rssi
	}
	p.RxSnr = snr
	p.TransportMechanism = pb.MeshPacket_TRANSPORT_LORA
	return p
}

// LastByte is the relay/next-hop byte for a node; 0x00 is reserved for "no preference".
func LastByte(n uint32) uint8 {
	if b := uint8(n); b != 0 {
		return b
	}
	return 0xFF
}

// NodeID formats a node number as "!xxxxxxxx".
func NodeID(n uint32) string { return fmt.Sprintf("!%08x", n) }

// ParseNodeID accepts "!xxxxxxxx", "0x…" or decimal.
func ParseNodeID(s string) (uint32, error) {
	s = strings.TrimSpace(s)
	var v uint64
	var err error
	switch {
	case strings.HasPrefix(s, "!"):
		v, err = strconv.ParseUint(s[1:], 16, 32)
	case strings.HasPrefix(strings.ToLower(s), "0x"):
		v, err = strconv.ParseUint(s[2:], 16, 32)
	default:
		v, err = strconv.ParseUint(s, 10, 32)
	}
	return uint32(v), err
}

// HopsAway mirrors getHopsAway: -1 when it can't be known.
func HopsAway(p *pb.MeshPacket) int {
	d, decoded := p.PayloadVariant.(*pb.MeshPacket_Decoded)
	if p.HopStart == 0 && !(decoded && d.Decoded.Bitfield != nil) {
		return -1
	}
	if p.HopStart < p.HopLimit {
		return -1
	}
	return int(p.HopStart - p.HopLimit)
}
