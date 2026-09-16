package nodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// BoardDriver is the radio driver of a board running stock Meshtastic firmware.
const BoardDriver = "meshtastic"

// BoardRadio is a board running stock Meshtastic firmware (on USB serial, or a network board's
// client API) used as a radio. The board is the radio's relay persona and does its own radio work.
// Other nodes reach the air through it, one hop behind, over its MQTT client proxy:
//
//   - A frame the host sends becomes an MQTT downlink to the board, which takes the packet as heard
//     via MQTT and repeats it on LoRa (with its hop limit one lower).
//   - Every packet the board hears (or sends itself) comes back as an MQTT uplink: a received frame,
//     with the RSSI and SNR the board measured.
//
// The board's MQTT module is given to RepeaterTastic for this (BoardSettings).
type BoardRadio struct {
	node   *Node
	device string
	frames chan radio.Frame
	logf   func(string, ...any)

	rx, tx, errs atomic.Uint32
	closeOnce    sync.Once
	stop         context.CancelFunc

	// contact returns what the host knows of a node (nil = nothing): a board takes a direct message
	// over MQTT only when it knows both ends.
	contactMu sync.Mutex
	contact   func(num uint32) *pb.User
}

// SetContacts gives the board a way to learn nodes the host knows.
func (b *BoardRadio) SetContacts(fn func(num uint32) *pb.User) {
	b.contactMu.Lock()
	b.contact = fn
	b.contactMu.Unlock()
}

// introduce adds a node to the board as a contact when the board hasn't heard of it.
func (b *BoardRadio) introduce(ctx context.Context, s mtclient.Snapshot, num uint32) {
	if _, known := s.Nodes[num]; known {
		if len(s.Nodes[num].GetUser().GetPublicKey()) == 32 {
			return
		}
	}
	b.contactMu.Lock()
	fn := b.contact
	b.contactMu.Unlock()
	if fn == nil {
		return
	}
	u := fn(num)
	if u == nil || len(u.GetPublicKey()) != 32 {
		return
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := b.node.Admin(actx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_AddContact{AddContact: &pb.SharedContact{NodeNum: num, User: u}}}); err != nil {
		b.logf("board: contact %s not added: %v", wire.NodeID(num), err)
	}
}

var _ radio.Radio = (*BoardRadio)(nil)

// OpenBoard connects to the board at device (a serial port, or host[:port]) and starts its radio.
// The node's state (for starts while the board is away) is kept in stateDir.
func OpenBoard(ctx context.Context, device, stateDir string, logf func(string, ...any)) (*BoardRadio, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if !mtclient.IsSerial(device) {
		if _, err := mtclient.TCPAddress(device); err != nil {
			return nil, err
		}
	}
	c := mtclient.New(mtclient.Options{Address: device, Logf: func(string, ...any) {}, ConfigTimeout: 30 * time.Second})
	return startBoard(ctx, c, device, stateDir, logf)
}

func startBoard(ctx context.Context, c *mtclient.Client, device, stateDir string, logf func(string, ...any)) (*BoardRadio, error) {
	ctx, stop := context.WithCancel(ctx)
	b := &BoardRadio{node: newNode(device, stateDir, c, logf), device: device, frames: make(chan radio.Frame, 256), logf: logf, stop: stop}
	b.node.SetExtraSettings(BoardSettings)
	events, unsubscribe := c.Subscribe(1024)
	if err := c.Start(ctx); err != nil {
		unsubscribe()
		stop()
		return nil, err
	}
	go b.uplinks(ctx, events, unsubscribe)
	return b, nil
}

// Node is the board: the radio's relay persona.
func (b *BoardRadio) Node() *Node { return b.node }

func (b *BoardRadio) uplinks(ctx context.Context, events <-chan mtclient.Event, unsubscribe func()) {
	defer unsubscribe()
	defer b.closeOnce.Do(func() { close(b.frames) })
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			m := e.FromRadio.GetMqttClientProxyMessage()
			if e.Kind != mtclient.Received || m == nil {
				continue
			}
			p := uplinkPacket(m)
			if p == nil {
				continue
			}
			frame, err := wire.EncodeFrame(p)
			if err != nil {
				b.errs.Add(1)
				continue
			}
			b.rx.Add(1)
			select {
			case b.frames <- radio.Frame{Data: frame, RSSI: int16(p.GetRxRssi()), SNR: p.GetRxSnr()}:
			default:
				b.errs.Add(1) // the host is behind: drop, as a busy radio would
			}
		}
	}
}

// uplinkPacket is the encrypted packet an uplink carries, or nil.
func uplinkPacket(m *pb.MqttClientProxyMessage) *pb.MeshPacket {
	var env pb.ServiceEnvelope
	if proto.Unmarshal(m.GetData(), &env) != nil {
		return nil
	}
	p := env.GetPacket()
	if p.GetEncrypted() == nil || p.GetFrom() == 0 {
		return nil // decoded (encryption off on the board) or not a mesh packet
	}
	return p
}

// Send hands a frame to the board as an MQTT downlink.
func (b *BoardRadio) Send(ctx context.Context, frame []byte) error {
	p := wire.DecodeFrame(frame, 0, 0)
	if p == nil {
		return errors.New("board: not a Meshtastic frame")
	}
	s := b.node.client.Snapshot()
	if !s.Connected {
		b.errs.Add(1)
		return mtclient.ErrNotConnected
	}
	channel, ok := downlinkChannel(s, p)
	if ok && channel == "PKI" {
		b.introduce(ctx, s, p.To)
	}
	if !ok {
		b.errs.Add(1)
		return fmt.Errorf("board: it has no channel with hash %d", p.Channel)
	}
	gateway := wire.NodeID(p.From)
	data, err := proto.Marshal(&pb.ServiceEnvelope{Packet: p, ChannelId: channel, GatewayId: gateway})
	if err != nil {
		return err
	}
	topic := "msh/" + s.Config.GetLora().GetRegion().String() + "/2/e/" + channel + "/" + gateway
	err = b.node.client.Send(&pb.ToRadio{PayloadVariant: &pb.ToRadio_MqttClientProxyMessage{MqttClientProxyMessage: &pb.MqttClientProxyMessage{
		Topic: topic, PayloadVariant: &pb.MqttClientProxyMessage_Data{Data: data}}}})
	if err != nil {
		b.errs.Add(1)
		return err
	}
	b.tx.Add(1)
	b.echo(s, p)
	return ctx.Err()
}

// echoDelay is how long after a downlink the board's repeat is played back.
const echoDelay = 400 * time.Millisecond

// echo plays back the board's repeat of a downlinked packet, as the sender would hear it on air:
// one hop lower, relayed by the board. The board doesn't uplink what came from MQTT, and a sender
// that never hears its packet repeated sends it again and then reports it failed. A board that
// doesn't repeat (client mute, rebroadcast none) or a packet with no hops left gets no echo.
func (b *BoardRadio) echo(s mtclient.Snapshot, p *pb.MeshPacket) {
	dev := s.Config.GetDevice()
	if p.HopLimit == 0 || p.To == s.NodeNum() || dev.GetRole() == pb.Config_DeviceConfig_CLIENT_MUTE ||
		dev.GetRebroadcastMode() == pb.Config_DeviceConfig_NONE || !s.Config.GetLora().GetTxEnabled() {
		return
	}
	e := proto.Clone(p).(*pb.MeshPacket)
	e.HopLimit--
	e.RelayNode = s.NodeNum() & 0xff
	frame, err := wire.EncodeFrame(e)
	if err != nil {
		return
	}
	time.AfterFunc(echoDelay, func() {
		defer func() { _ = recover() }() // the radio closed meanwhile
		select {
		case b.frames <- radio.Frame{Data: frame, RSSI: loopRSSI, SNR: loopSNR}:
		default:
		}
	})
}

// downlinkChannel is the MQTT channel id the board takes a packet on: PKI for a direct message
// under a node's key (channel hash 0), else the board's channel with the packet's hash.
func downlinkChannel(s mtclient.Snapshot, p *pb.MeshPacket) (string, bool) {
	if p.Channel == 0 && p.To != wire.Broadcast {
		return "PKI", true
	}
	for id, hash := range boardChannels(s) {
		if uint32(hash) == p.Channel {
			return id, true
		}
	}
	return "", false
}

// boardChannels maps the board's enabled channels, by the id MQTT knows them by, to their hashes.
func boardChannels(s mtclient.Snapshot) map[string]uint8 {
	preset := phy.Presets[s.Config.GetLora().GetModemPreset()].Display
	out := map[string]uint8{}
	for _, ch := range s.Channels {
		if ch == nil || ch.Role == pb.Channel_DISABLED {
			continue
		}
		st := ch.GetSettings()
		name := st.GetName()
		if name == "" {
			name = preset
		}
		out[name] = wire.ChannelHash(name, wire.ExpandPSK(st.GetPsk()), st.GetUseAead())
	}
	return out
}

// BoardSettings are the changes a board needs to carry other nodes: its MQTT module through the
// client proxy, carrying encrypted packets, with a private server address (so packets without the
// OK-to-MQTT flag are still passed on), and uplink and downlink on every channel.
func BoardSettings(s mtclient.Snapshot) []*pb.AdminMessage {
	var msgs []*pb.AdminMessage
	if cur := s.ModuleConfig.GetMqtt(); cur != nil || s.ModuleConfig != nil {
		m := &pb.ModuleConfig_MQTTConfig{}
		if cur != nil {
			m = proto.Clone(cur).(*pb.ModuleConfig_MQTTConfig)
		}
		m.Enabled, m.ProxyToClientEnabled, m.EncryptionEnabled, m.JsonEnabled = true, true, true, false
		m.Address = "127.0.0.1"
		m.MapReportingEnabled = false
		if !proto.Equal(m, cur) {
			msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetModuleConfig{
				SetModuleConfig: &pb.ModuleConfig{PayloadVariant: &pb.ModuleConfig_Mqtt{Mqtt: m}}}})
		}
	}
	for _, ch := range s.Channels {
		if ch == nil || ch.Role == pb.Channel_DISABLED || (ch.GetSettings().GetUplinkEnabled() && ch.GetSettings().GetDownlinkEnabled()) {
			continue
		}
		c := proto.Clone(ch).(*pb.Channel)
		if c.Settings == nil {
			c.Settings = &pb.ChannelSettings{}
		}
		c.Settings.UplinkEnabled, c.Settings.DownlinkEnabled = true, true
		msgs = append(msgs, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetChannel{SetChannel: c}})
	}
	return msgs
}

// PrepareChannel sets what a board needs on a channel written to it: MQTT uplink and downlink,
// so the identities behind it can use the channel.
func (n *Node) PrepareChannel(ch *pb.Channel) {
	n.mu.Lock()
	board := n.extra != nil
	n.mu.Unlock()
	if !board || ch == nil || ch.Role == pb.Channel_DISABLED {
		return
	}
	if ch.Settings == nil {
		ch.Settings = &pb.ChannelSettings{}
	}
	ch.Settings.UplinkEnabled, ch.Settings.DownlinkEnabled = true, true
}

// Configure does nothing: the board's settings are written as the relay's (Node.ApplyConfig).
func (b *BoardRadio) Configure(context.Context, radio.Config) error { return nil }

func (b *BoardRadio) Frames() <-chan radio.Frame { return b.frames }

// ChannelBusy is false: the board listens before it talks itself.
func (b *BoardRadio) ChannelBusy(context.Context) (bool, error) { return false, nil }

func (b *BoardRadio) Info() radio.Info {
	s := b.node.client.Snapshot()
	name := "Meshtastic node"
	if hw := s.Metadata.GetHwModel(); hw != pb.HardwareModel_UNSET {
		name = "Meshtastic " + strings.ReplaceAll(hw.String(), "_", " ")
	}
	fw := ""
	if v := s.Metadata.GetFirmwareVersion(); v != "" {
		fw = "Meshtastic " + v
	}
	return radio.Info{Driver: BoardDriver, Device: b.device, Firmware: fw, Name: name}
}

func (b *BoardRadio) Stats(context.Context) radio.Stats {
	s := b.node.client.Snapshot()
	return radio.Stats{RxPackets: b.rx.Load(), TxPackets: b.tx.Load(), Errors: b.errs.Load(),
		Connected: s.Connected, Reconnects: s.Reconnects}
}

// Close disconnects from the board.
func (b *BoardRadio) Close() error {
	b.stop()
	return b.node.client.Close()
}
