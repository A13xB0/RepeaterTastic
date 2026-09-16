package mesh

import (
	"math"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// FixedPosition is a site's surveyed location, broadcast like a fixed-position node.
type FixedPosition struct {
	Latitude, Longitude float64 // degrees; both 0 = no position
	Altitude            int32   // metres above sea level
	// PrecisionBits keeps that many bits of latitude/longitude (32 = exact, 13 ≈ 3 km).
	PrecisionBits uint32
	// Interval between broadcasts; 0 = 3 h, never less than 30 min.
	Interval time.Duration
	// AllIdentities broadcasts from every identity; otherwise only the relay persona does.
	AllIdentities bool
}

const (
	positionDefaultInterval = 3 * time.Hour
	positionMinInterval     = 30 * time.Minute
	positionReplySuppress   = 3 * time.Minute
)

// Set reports whether a position is configured.
func (p FixedPosition) Set() bool { return p.Latitude != 0 || p.Longitude != 0 }

func (p FixedPosition) interval() time.Duration {
	switch {
	case p.Interval <= 0:
		return positionDefaultInterval
	case p.Interval < positionMinInterval:
		return positionMinInterval
	}
	return p.Interval
}

func (p FixedPosition) bits() uint32 {
	if p.PrecisionBits == 0 || p.PrecisionBits > 32 {
		return 32
	}
	return p.PrecisionBits
}

// proto is the Position as broadcast: coordinates truncated to the precision, centred in
// the dropped range as the firmware does.
func (p FixedPosition) proto(now time.Time) *pb.Position {
	bits := p.bits()
	lat := truncateCoord(int32(math.Round(p.Latitude*1e7)), bits)
	lon := truncateCoord(int32(math.Round(p.Longitude*1e7)), bits)
	alt := p.Altitude
	t := uint32(now.Unix())
	return &pb.Position{LatitudeI: &lat, LongitudeI: &lon, Altitude: &alt, Time: t,
		LocationSource: pb.Position_LOC_MANUAL, PrecisionBits: bits}
}

func truncateCoord(v int32, bits uint32) int32 {
	if bits >= 32 {
		return v
	}
	mask := uint32(math.MaxUint32) << (32 - bits)
	return int32(uint32(v)&mask + (1 << (31 - bits)))
}

// Hardware is the hardware model this host's identities advertise: the configured model, or
// with none (or "auto") the modem's own board, falling back to PORTDUINO for a software node.
func (h *Host) Hardware() pb.HardwareModel {
	if m := h.Config().HwModel; m != pb.HardwareModel_UNSET {
		return m
	}
	return hardwareFromModem(h.radio.Info().Name)
}

// hardwareFromModem maps the name a KISS modem reports to Meshtastic's hardware model.
func hardwareFromModem(name string) pb.HardwareModel {
	n := strings.ToLower(strings.ReplaceAll(name, " ", ""))
	switch {
	case strings.Contains(n, "heltecv4"):
		return pb.HardwareModel_HELTEC_V4
	case strings.Contains(n, "heltecv3"):
		return pb.HardwareModel_HELTEC_V3
	case strings.Contains(n, "wirelesstracker"):
		return pb.HardwareModel_HELTEC_WIRELESS_TRACKER
	case strings.Contains(n, "rak4631"):
		return pb.HardwareModel_RAK4631
	case strings.Contains(n, "xiao") && strings.Contains(n, "nrf52"):
		return pb.HardwareModel_XIAO_NRF52_KIT
	case strings.Contains(n, "xiao"):
		return pb.HardwareModel_SEEED_XIAO_S3
	case strings.Contains(n, "t-echo"), strings.Contains(n, "techo"):
		return pb.HardwareModel_T_ECHO
	case strings.Contains(n, "t-beam"), strings.Contains(n, "tbeam"):
		return pb.HardwareModel_TBEAM
	}
	return pb.HardwareModel_PORTDUINO
}

// PositionFor is the position an identity broadcasts (see positionFor).
func (h *Host) PositionFor(id *Identity) (FixedPosition, bool) { return h.positionFor(id) }

// positionFor is the position an identity broadcasts: its own fixed position if it has one,
// otherwise the radio's site position when the site policy covers it.
func (h *Host) positionFor(id *Identity) (FixedPosition, bool) {
	site := h.Config().Position
	if own, ok := id.FixedPosition(); ok {
		p := FixedPosition{Latitude: own.Latitude, Longitude: own.Longitude, Altitude: own.Altitude,
			PrecisionBits: site.PrecisionBits, Interval: site.Interval}
		if secs := id.PositionInterval(); secs > 0 {
			p.Interval = time.Duration(secs) * time.Second
		}
		return p, true
	}
	if site.Set() && (id.IsRelay || site.AllIdentities) {
		if secs := id.PositionInterval(); secs > 0 {
			site.Interval = time.Duration(secs) * time.Second
		}
		return site, true
	}
	return FixedPosition{}, false
}

// broadcastsPosition reports whether this identity broadcasts a position.
func (h *Host) broadcastsPosition(id *Identity) bool {
	_, ok := h.positionFor(id)
	return ok
}

// RecordOwnPositions puts each identity's position in the node DB, so apps connected to it
// (and the node map) show where it is.
func (h *Host) RecordOwnPositions() {
	now := time.Now()
	for _, id := range h.Identities() {
		pos, ok := h.positionFor(id)
		var p *pb.Position
		if ok {
			p = pos.proto(now)
		}
		h.DB.Update(id.NodeNum, func(e *NodeEntry) { e.Position = p })
	}
}

// periodicPosition broadcasts each identity's position on its interval.
func (h *Host) periodicPosition(now time.Time) {
	for _, id := range h.Identities() {
		pos, ok := h.positionFor(id)
		if !id.Enabled || !ok || id.Remote() != nil {
			continue
		}
		id.mu.Lock()
		if id.nextPosition.IsZero() {
			// first broadcast a little after the NodeInfo (or soon after a change), staggered per identity
			first := id.nextNodeInfo.Add(45 * time.Second)
			if first.Before(now) {
				first = now.Add(time.Duration(10+id.NodeNum%50) * time.Second)
			}
			id.nextPosition = first
		}
		due := !now.Before(id.nextPosition)
		if due {
			id.nextPosition = now.Add(pos.interval())
		}
		id.mu.Unlock()
		if !due || h.Air.ChannelUtilPercent(now) > 40 || !h.radioOK.Load() {
			continue
		}
		if limit := h.dutyLimit(); limit < 100 && h.Air.TxPercent(now) > limit/2 {
			continue
		}
		h.sendPosition(id, pos, wire.Broadcast, 0, 0)
	}
}

// replyPosition answers a position request addressed to one of our identities.
func (h *Host) replyPosition(id *Identity, req *pb.MeshPacket) {
	pos, ok := h.positionFor(id)
	if !ok {
		return
	}
	now := time.Now()
	id.mu.Lock()
	if now.Sub(id.lastPositionReply) < positionReplySuppress {
		id.mu.Unlock()
		return
	}
	id.lastPositionReply = now
	id.mu.Unlock()
	h.sendPosition(id, pos, req.From, int(req.Channel), req.Id)
}

func (h *Host) sendPosition(id *Identity, pos FixedPosition, to uint32, channel int, requestID uint32) {
	cfg := h.Config()
	payload, err := proto.Marshal(pos.proto(time.Now()))
	if err != nil {
		return
	}
	p := &pb.MeshPacket{To: to, Channel: uint32(channel), Priority: pb.MeshPacket_BACKGROUND,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_POSITION_APP, Payload: payload,
			RequestId: requestID}}}
	p.From = id.NodeNum
	p.Id = wire.RandomPacketID()
	p.HopLimit = cfg.HopLimit
	if err := h.transmit(id, p, false); err != nil {
		h.log.Debug("position not sent", "identity", id.NodeID(), "err", err)
	}
}
