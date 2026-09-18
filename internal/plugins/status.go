package plugins

import (
	"context"
	"time"

	pluginv1 "github.com/ScotMesh/RepeaterTastic/api/plugin/v1"
	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
)

// Radio tells a plugin what a radio is; RadioStatus tells it what the radio is
// doing. Everything here is already computed for the GUI's status page — a
// plugin that wanted airtime or a noise floor previously had no way to ask.

// statusInterval is how often StatusEvent is pushed to plugins holding
// status.read. Slow enough not to matter, quick enough that a dashboard does
// not look stale.
const statusInterval = 30 * time.Second

func (h *hostServer) GetStatus(ctx context.Context, req *pluginv1.GetStatusRequest) (*pluginv1.GetStatusResponse, error) {
	if _, err := h.granted(ctx, permStatusRead); err != nil {
		return nil, err
	}
	out := &pluginv1.GetStatusResponse{}
	for _, r := range h.m.opt.Radios {
		if req.RadioId != "" && r.ID != req.RadioId {
			continue
		}
		out.Radios = append(out.Radios, radioStatus(ctx, r))
	}
	return out, nil
}

func radioStatus(ctx context.Context, r Radio) *pluginv1.RadioStatus {
	h := r.Host
	now := time.Now()
	st := h.Radio().Stats(ctx)
	c := &h.Counters

	duty := h.RadioParams().Region.DutyCyclePct
	if cfg := h.Config(); cfg.OverrideDutyCycle {
		duty = 100
	} else if cfg.DutyCyclePct > 0 {
		duty = cfg.DutyCyclePct
	}

	heard := h.DB.HeardSince(now.Add(-2 * time.Hour))

	return &pluginv1.RadioStatus{
		RadioId:         r.ID,
		Connected:       st.Connected && h.RadioConfigured(),
		NoiseFloorDbm:   int32(st.NoiseFloorDBm),
		AirtimeTxPct:    h.Air.TxPercent(now),
		DutyLimitPct:    duty,
		ChannelUtilPct:  h.Air.ChannelUtilPercent(now),
		Queue:           uint32(h.QueueLen()),
		Rx:              c.Rx.Load(),
		Tx:              c.Tx.Load(),
		RxDupe:          c.RxDupe.Load(),
		RxUndecryptable: c.RxUndecryptable.Load(),
		RxBad:           c.RxBad.Load(),
		TxFailed:        c.TxFailed.Load(),
		AckOk:           c.AckOK.Load(),
		AckFail:         c.AckFail.Load(),
		DroppedDuty:     c.DroppedDuty.Load(),
		UptimeS:         int64(now.Sub(h.Started()).Seconds()),
		NodesHeard:      uint32(heard),
	}
}

// statusEvent builds the pushed form, for plugins that would rather be told
// than ask.
func (m *Manager) statusEvent(ctx context.Context) *pluginv1.StatusEvent {
	e := &pluginv1.StatusEvent{}
	for _, r := range m.opt.Radios {
		e.Radios = append(e.Radios, radioStatus(ctx, r))
	}
	return e
}

// pumpStatus pushes a StatusEvent on a period, covering every radio at once —
// unlike the per-radio event pumps, because a status is a picture of the site.
func (m *Manager) pumpStatus(ctx context.Context, sess *session) {
	send := func() {
		sess.send(&pluginv1.HostMessage{
			Msg: &pluginv1.HostMessage_StatusEvent{StatusEvent: m.statusEvent(ctx)},
		})
	}
	send() // so a plugin has figures immediately rather than after the first period
	t := time.NewTicker(statusInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.done:
			return
		case <-t.C:
			send()
		}
	}
}

var _ = mesh.Host{}
