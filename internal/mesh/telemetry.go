package mesh

import (
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/wire"
	"github.com/A13xB0/RepeaterTastic/pb"
)

const telemetryMinInterval = 30 * time.Minute

// periodicTelemetry broadcasts the relay persona's DeviceMetrics on the configured interval, as a
// mains-powered node does: uptime and this radio's channel and TX airtime use.
func (h *Host) periodicTelemetry(now time.Time) {
	iv := h.Config().TelemetryInterval
	relay := h.Relay()
	if iv <= 0 || relay == nil {
		h.nextTelemetry = time.Time{}
		return
	}
	if iv < telemetryMinInterval {
		iv = telemetryMinInterval
	}
	if h.nextTelemetry.IsZero() {
		h.nextTelemetry = now.Add(2*time.Minute + time.Duration(relay.NodeNum%60)*time.Second)
		return
	}
	if now.Before(h.nextTelemetry) {
		return
	}
	h.nextTelemetry = now.Add(iv)
	if !h.radioOK.Load() || h.Air.ChannelUtilPercent(now) > 40 {
		return
	}
	if limit := h.dutyLimit(); limit < 100 && h.Air.TxPercent(now) > limit/2 {
		return
	}
	h.sendTelemetry(relay, h.deviceMetrics(now))
}

func (h *Host) deviceMetrics(now time.Time) *pb.Telemetry {
	battery := uint32(101) // 101 = external power, as the firmware reports it
	chUtil := float32(h.Air.ChannelUtilPercent(now))
	airTx := float32(h.Air.TxPercent(now))
	uptime := uint32(now.Sub(h.started).Seconds())
	return &pb.Telemetry{Time: uint32(now.Unix()), Variant: &pb.Telemetry_DeviceMetrics{DeviceMetrics: &pb.DeviceMetrics{
		BatteryLevel: &battery, ChannelUtilization: &chUtil, AirUtilTx: &airTx, UptimeSeconds: &uptime}}}
}

func (h *Host) sendTelemetry(id *Identity, t *pb.Telemetry) {
	payload, err := proto.Marshal(t)
	if err != nil {
		return
	}
	p := &pb.MeshPacket{To: wire.Broadcast, Priority: pb.MeshPacket_BACKGROUND,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TELEMETRY_APP, Payload: payload}}}
	p.From = id.NodeNum
	p.Id = wire.RandomPacketID()
	p.HopLimit = h.Config().HopLimit
	if err := h.transmit(id, p, false); err != nil {
		h.log.Debug("telemetry not sent", "identity", id.NodeID(), "err", err)
	}
}
