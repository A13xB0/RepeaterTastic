// Package mtclienttest is a fake Meshtastic node for tests: it answers the client API handshake
// and admin messages the way the firmware does, closely enough for clients of mtclient.
package mtclienttest

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/ScotMesh/RepeaterTastic/internal/mtclient"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Node is one fake node. Change its fields under Update; a new connection sees the change.
type Node struct {
	mu       sync.Mutex
	conn     net.Conn
	dials    int
	toRadio  []*pb.ToRadio
	admins   []*pb.AdminMessage
	num      uint32
	owner    *pb.User
	metadata *pb.DeviceMetadata
	config   *pb.LocalConfig
	modules  *pb.LocalModuleConfig
	channels []*pb.Channel
	others   []*pb.NodeInfo
	noise    bool
	silent   bool
	reboot   bool
}

// SetNoise writes console text between frames, as a serial board does.
func (n *Node) SetNoise(on bool) { n.mu.Lock(); n.noise = on; n.mu.Unlock() }

// SetSilent stops the node finishing the handshake.
func (n *Node) SetSilent(on bool) { n.mu.Lock(); n.silent = on; n.mu.Unlock() }

// SetRebootOnCommit drops the connection after commit_edit_settings, as a board does.
func (n *Node) SetRebootOnCommit(on bool) { n.mu.Lock(); n.reboot = on; n.mu.Unlock() }

// New returns a node numbered num on EU_868 LongFast with a default primary channel.
func New(num uint32) *Node {
	return &Node{
		num:      num,
		owner:    &pb.User{LongName: "Fake", ShortName: "FAKE", HwModel: pb.HardwareModel_HELTEC_V3, PublicKey: make([]byte, 32)},
		metadata: &pb.DeviceMetadata{FirmwareVersion: "2.8.0.fake", HwModel: pb.HardwareModel_HELTEC_V3},
		config: &pb.LocalConfig{
			Device:   &pb.Config_DeviceConfig{Role: pb.Config_DeviceConfig_CLIENT},
			Lora:     &pb.Config_LoRaConfig{Region: pb.Config_LoRaConfig_EU_868, UsePreset: true, ModemPreset: pb.Config_LoRaConfig_LONG_FAST, HopLimit: 3, TxEnabled: true},
			Security: &pb.Config_SecurityConfig{PublicKey: make([]byte, 32)},
		},
		modules: &pb.LocalModuleConfig{Mqtt: &pb.ModuleConfig_MQTTConfig{}},
		channels: []*pb.Channel{
			{Index: 0, Role: pb.Channel_PRIMARY, Settings: &pb.ChannelSettings{Psk: []byte{1}}},
			{Index: 1, Role: pb.Channel_DISABLED, Settings: &pb.ChannelSettings{}},
		},
	}
}

// Num is the node's number.
func (n *Node) Num() uint32 { n.mu.Lock(); defer n.mu.Unlock(); return n.num }

// Update changes the node's state under its lock.
func (n *Node) Update(fn func(s *State)) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s := &State{Num: n.num, Owner: n.owner, Config: n.config, Modules: n.modules, Channels: n.channels, Others: n.others}
	fn(s)
	n.num, n.owner, n.config, n.modules, n.channels, n.others = s.Num, s.Owner, s.Config, s.Modules, s.Channels, s.Others
}

// State is what Update may change.
type State struct {
	Num      uint32
	Owner    *pb.User
	Config   *pb.LocalConfig
	Modules  *pb.LocalModuleConfig
	Channels []*pb.Channel
	Others   []*pb.NodeInfo
}

// Config returns a copy of the node's configuration.
func (n *Node) Config() *pb.LocalConfig {
	n.mu.Lock()
	defer n.mu.Unlock()
	return proto.Clone(n.config).(*pb.LocalConfig)
}

// Channels returns copies of the node's channels.
func (n *Node) Channels() []*pb.Channel {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]*pb.Channel, len(n.channels))
	for i, c := range n.channels {
		out[i] = proto.Clone(c).(*pb.Channel)
	}
	return out
}

// Dial is an mtclient.Options.Dial that connects to the node.
func (n *Node) Dial(context.Context) (io.ReadWriteCloser, error) {
	host, node := net.Pipe()
	n.mu.Lock()
	n.dials++
	n.conn = node
	n.mu.Unlock()
	go n.serve(node)
	return host, nil
}

// Dials counts connections.
func (n *Node) Dials() int { n.mu.Lock(); defer n.mu.Unlock(); return n.dials }

// Push sends a FromRadio on the current connection.
func (n *Node) Push(fr *pb.FromRadio) {
	n.mu.Lock()
	c := n.conn
	n.mu.Unlock()
	if c != nil {
		n.send(c, fr)
	}
}

// Deliver pushes a packet as received by the node.
func (n *Node) Deliver(p *pb.MeshPacket) {
	n.Push(&pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: p}})
}

// Drop closes the current connection (a reboot or an unplugged cable).
func (n *Node) Drop() {
	n.mu.Lock()
	c := n.conn
	n.mu.Unlock()
	if c != nil {
		c.Close()
	}
}

// Received lists every ToRadio the node got.
func (n *Node) Received() []*pb.ToRadio {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]*pb.ToRadio(nil), n.toRadio...)
}

// Packets lists the MeshPackets the node got, admin messages to itself excluded.
func (n *Node) Packets() []*pb.MeshPacket {
	var out []*pb.MeshPacket
	for _, tr := range n.Received() {
		if p := tr.GetPacket(); p != nil && !(p.To == n.Num() && p.GetDecoded().GetPortnum() == pb.PortNum_ADMIN_APP) {
			out = append(out, p)
		}
	}
	return out
}

// Admins lists the admin messages the node got, in order.
func (n *Node) Admins() []*pb.AdminMessage {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]*pb.AdminMessage(nil), n.admins...)
}

func (n *Node) send(conn io.Writer, fr *pb.FromRadio) {
	b, _ := proto.Marshal(fr)
	frame, _ := mtclient.AppendFrame(nil, b)
	n.mu.Lock()
	noise := n.noise
	n.mu.Unlock()
	if noise {
		frame = append([]byte("INFO | 12:00:00 [Router] \x94 console text\r\n"), frame...)
	}
	_, _ = conn.Write(frame)
}

func (n *Node) serve(conn net.Conn) {
	_ = mtclient.ReadFrames(conn, func(b []byte) bool {
		tr := &pb.ToRadio{}
		if proto.Unmarshal(b, tr) != nil {
			return true
		}
		n.mu.Lock()
		n.toRadio = append(n.toRadio, tr)
		silent := n.silent
		n.mu.Unlock()
		switch v := tr.PayloadVariant.(type) {
		case *pb.ToRadio_WantConfigId:
			if !silent {
				for _, fr := range n.handshake(v.WantConfigId) {
					n.send(conn, fr)
				}
			}
		case *pb.ToRadio_Packet:
			return n.answer(conn, v.Packet)
		}
		return true
	})
	conn.Close()
}

func (n *Node) handshake(nonce uint32) []*pb.FromRadio {
	n.mu.Lock()
	defer n.mu.Unlock()
	owner := proto.Clone(n.owner).(*pb.User)
	out := []*pb.FromRadio{
		// A completion for somebody else's request comes first and must be ignored.
		{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce + 1}},
		{PayloadVariant: &pb.FromRadio_MyInfo{MyInfo: &pb.MyNodeInfo{MyNodeNum: n.num}}},
		{PayloadVariant: &pb.FromRadio_Metadata{Metadata: proto.Clone(n.metadata).(*pb.DeviceMetadata)}},
		{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: &pb.NodeInfo{Num: n.num, User: owner}}},
	}
	for _, o := range n.others {
		out = append(out, &pb.FromRadio{PayloadVariant: &pb.FromRadio_NodeInfo{NodeInfo: proto.Clone(o).(*pb.NodeInfo)}})
	}
	for _, c := range n.channels {
		out = append(out, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Channel{Channel: proto.Clone(c).(*pb.Channel)}})
	}
	for _, c := range oneofParts(n.config, &pb.Config{}) {
		out = append(out, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Config{Config: c.(*pb.Config)}})
	}
	for _, c := range oneofParts(n.modules, &pb.ModuleConfig{}) {
		out = append(out, &pb.FromRadio{PayloadVariant: &pb.FromRadio_ModuleConfig{ModuleConfig: c.(*pb.ModuleConfig)}})
	}
	return append(out, &pb.FromRadio{PayloadVariant: &pb.FromRadio_ConfigCompleteId{ConfigCompleteId: nonce}})
}

// oneofParts splits a LocalConfig (LocalModuleConfig) into the Config (ModuleConfig) messages the
// firmware sends, one per set section.
func oneofParts(local, kind proto.Message) []proto.Message {
	var out []proto.Message
	lr := local.ProtoReflect()
	cd := kind.ProtoReflect().Descriptor()
	lr.Range(func(f protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if f.Message() == nil {
			return true
		}
		cf := cd.Fields().ByName(f.Name())
		if cf == nil || cf.ContainingOneof() == nil {
			return true
		}
		c := kind.ProtoReflect().New()
		c.Set(cf, protoreflect.ValueOfMessage(proto.Clone(v.Message().Interface()).ProtoReflect()))
		out = append(out, c.Interface())
		return true
	})
	return out
}

// answer handles packets to the node itself; it returns false to end the session (a reboot).
func (n *Node) answer(conn io.Writer, p *pb.MeshPacket) bool {
	d := p.GetDecoded()
	if p.To != n.Num() || d.GetPortnum() != pb.PortNum_ADMIN_APP {
		return true
	}
	am := &pb.AdminMessage{}
	if proto.Unmarshal(d.GetPayload(), am) != nil {
		return true
	}
	n.mu.Lock()
	n.admins = append(n.admins, am)
	num := n.num
	n.mu.Unlock()
	reply := func(port pb.PortNum, m proto.Message) {
		b, _ := proto.Marshal(m)
		n.send(conn, &pb.FromRadio{PayloadVariant: &pb.FromRadio_Packet{Packet: &pb.MeshPacket{From: num, To: num, Id: mtclient.NewPacketID(),
			PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: port, Payload: b, RequestId: p.Id}}}}})
	}
	ack := func(reason pb.Routing_Error) {
		reply(pb.PortNum_ROUTING_APP, &pb.Routing{Variant: &pb.Routing_ErrorReason{ErrorReason: reason}})
	}
	switch v := am.PayloadVariant.(type) {
	case *pb.AdminMessage_GetOwnerRequest:
		ack(pb.Routing_NONE) // the firmware acks first; the response must still be awaited
		n.mu.Lock()
		owner := proto.Clone(n.owner).(*pb.User)
		n.mu.Unlock()
		reply(pb.PortNum_ADMIN_APP, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerResponse{GetOwnerResponse: owner}})
	case *pb.AdminMessage_SetOwner:
		n.mu.Lock()
		n.owner = v.SetOwner
		n.mu.Unlock()
		ack(pb.Routing_NONE)
	case *pb.AdminMessage_SetConfig:
		n.mu.Lock()
		mergeConfig(n.config, v.SetConfig)
		n.mu.Unlock()
		ack(pb.Routing_NONE)
	case *pb.AdminMessage_SetChannel:
		n.mu.Lock()
		i := int(v.SetChannel.GetIndex())
		for len(n.channels) <= i {
			n.channels = append(n.channels, &pb.Channel{Index: int32(len(n.channels)), Role: pb.Channel_DISABLED})
		}
		n.channels[i] = v.SetChannel
		n.mu.Unlock()
		ack(pb.Routing_NONE)
	case *pb.AdminMessage_BeginEditSettings:
		ack(pb.Routing_NONE)
	case *pb.AdminMessage_CommitEditSettings:
		ack(pb.Routing_NONE)
		n.mu.Lock()
		reboot := n.reboot
		n.mu.Unlock()
		return !reboot
	case *pb.AdminMessage_RebootSeconds:
		ack(pb.Routing_NOT_AUTHORIZED)
	case *pb.AdminMessage_FactoryResetDevice:
		// never answered: lets clients test their timeouts
	default:
		if strings.HasPrefix(string(am.ProtoReflect().WhichOneof(am.ProtoReflect().Descriptor().Oneofs().ByName("payload_variant")).Name()), "get_") {
			return true
		}
		ack(pb.Routing_NONE)
	}
	return true
}

func mergeConfig(dst *pb.LocalConfig, c *pb.Config) {
	cr := c.ProtoReflect()
	f := cr.WhichOneof(cr.Descriptor().Oneofs().ByName("payload_variant"))
	if f == nil {
		return
	}
	df := dst.ProtoReflect().Descriptor().Fields().ByName(f.Name())
	if df != nil {
		dst.ProtoReflect().Set(df, protoreflect.ValueOfMessage(proto.Clone(cr.Get(f).Message().Interface()).ProtoReflect()))
	}
}
