package mtclient

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

// Live tests talk to real radio-less meshtasticd instances (Lora: Module: sim, no UDP):
//
//	RT_TEST_MESHTASTICD=127.0.0.1:4420,127.0.0.1:4421 go test ./internal/mtclient -run Live -v
func liveAddrs(t *testing.T, n int) []string {
	t.Helper()
	v := os.Getenv("RT_TEST_MESHTASTICD")
	if v == "" {
		t.Skip("RT_TEST_MESHTASTICD not set")
	}
	addrs := strings.Split(v, ",")
	if len(addrs) < n {
		t.Skipf("need %d meshtasticd addresses", n)
	}
	return addrs[:n]
}

func liveClient(t *testing.T, addr string) *Client {
	t.Helper()
	c := New(Options{Address: addr, Logf: t.Logf})
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.WaitReady(ctx); err != nil {
		t.Fatalf("%s: %v", addr, err)
	}
	return c
}

func TestLiveHandshake(t *testing.T) {
	addr := liveAddrs(t, 1)[0]
	c := liveClient(t, addr)
	s := c.Snapshot()
	t.Logf("node !%08x fw %s role %s region %s preset %s", s.NodeNum(), s.Metadata.GetFirmwareVersion(),
		s.Config.GetDevice().GetRole(), s.Config.GetLora().GetRegion(), s.Config.GetLora().GetModemPreset())
	t.Logf("self %v, %d nodes, %d channels, pubkey %d bytes", s.Self().GetUser().GetLongName(), len(s.Nodes), len(s.Channels), len(s.Config.GetSecurity().GetPublicKey()))
	if s.NodeNum() == 0 || s.Config.GetLora() == nil || len(s.Channels) == 0 {
		t.Fatalf("incomplete snapshot: %+v", s)
	}
	ctx := context.Background()
	r, err := c.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("owner %v", r.GetGetOwnerResponse())
}

// TestLiveBridge links two instances the way the air bridge will: every SIMULATOR_APP frame one
// transmits is injected into the other with a made-up RSSI/SNR.
func TestLiveBridge(t *testing.T) {
	addrs := liveAddrs(t, 2)
	a, b := liveClient(t, addrs[0]), liveClient(t, addrs[1])
	ctx := context.Background()
	for _, c := range []*Client{a, b} {
		setRegionEU868(t, ctx, c)
	}

	ea, stopA := a.Subscribe(256)
	defer stopA()
	eb, stopB := b.Subscribe(256)
	defer stopB()
	got := make(chan *pb.MeshPacket, 64)
	go bridgeAir(t, "A", ea, b, -60, got)
	go bridgeAir(t, "B", eb, a, -70, got)

	// Make them learn each other: each broadcasts its NodeInfo.
	sa := a.Snapshot()
	hop := sa.Config.GetLora().GetHopLimit()
	for _, c := range []*Client{a, b} {
		sendNodeInfo(t, c, hop)
		time.Sleep(time.Second)
	}
	bNum := b.Snapshot().NodeNum()
	for i := 0; i < 50 && len(a.Snapshot().Nodes[bNum].GetUser().GetPublicKey()) == 0; i++ {
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("A knows B: %v, key %d bytes", a.Snapshot().Nodes[bNum].GetUser().GetLongName(), len(a.Snapshot().Nodes[bNum].GetUser().GetPublicKey()))
	_, err := a.SendPacket(&pb.MeshPacket{To: 0xffffffff, HopLimit: hop,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hello channel")}}})
	if err != nil {
		t.Fatal(err)
	}
	awaitTexts(t, a, b, got, hop)
	time.Sleep(3 * time.Second) // let the DM's ack cross
	for len(got) > 0 {
		p := <-got
		t.Logf("later: from !%08x to !%08x port %s", p.GetFrom(), p.GetTo(), p.GetDecoded().GetPortnum())
	}
}

// setRegionEU868 sets c's node to EU_868 with a preset, unless it's there already, and waits for
// it to come back configured.
func setRegionEU868(t *testing.T, ctx context.Context, c *Client) {
	t.Helper()
	s := c.Snapshot()
	if s.Config.GetLora().GetRegion() == pb.Config_LoRaConfig_EU_868 {
		return
	}
	ev, stop := c.Subscribe(16)
	lora := proto.Clone(s.Config.GetLora()).(*pb.Config_LoRaConfig)
	lora.Region = pb.Config_LoRaConfig_EU_868
	lora.UsePreset = true
	if _, err := c.Admin(ctx, &pb.AdminMessage{PayloadVariant: &pb.AdminMessage_SetConfig{SetConfig: &pb.Config{PayloadVariant: &pb.Config_Lora{Lora: lora}}}}); err != nil {
		t.Fatalf("set region: %v", err)
	}
	start := time.Now()
	if !gotKind(ev, Disconnected, 10*time.Second) {
		c.Reconnect() // applied without a reboot (2.8)
	}
	waitKind(t, ev, Configured, 30*time.Second)
	stop()
	t.Logf("!%08x rebooted in %v, region %s", c.Snapshot().NodeNum(), time.Since(start).Round(100*time.Millisecond), c.Snapshot().Config.GetLora().GetRegion())
}

// bridgeAir injects every SIMULATOR_APP frame (or raw ciphertext from an older SimRadio) from one
// node into another with a made-up RSSI, and passes packets delivered to the client on to got.
func bridgeAir(t *testing.T, name string, from <-chan Event, to *Client, rssi int32, got chan<- *pb.MeshPacket) {
	for e := range from {
		p := e.FromRadio.GetPacket()
		if p == nil {
			continue
		}
		d := p.GetDecoded()
		if enc := p.GetEncrypted(); enc != nil && p.GetRxRssi() == 0 {
			injectCiphertext(t, name, p, to, rssi)
			continue
		}
		if d.GetPortnum() != pb.PortNum_SIMULATOR_APP {
			if p.GetFrom() != 0 {
				got <- p
			}
			continue
		}
		injectSimulated(t, name, p, to, rssi)
	}
}

// injectCiphertext wraps ciphertext an older SimRadio handed straight to the client as a
// SIMULATOR_APP frame and injects it.
func injectCiphertext(t *testing.T, name string, p *pb.MeshPacket, to *Client, rssi int32) {
	enc := p.GetEncrypted()
	t.Logf("%s air (raw encrypted): from !%08x to !%08x id %08x hop %d/%d pki %v %d bytes", name, p.GetFrom(), p.GetTo(), p.GetId(), p.GetHopLimit(), p.GetHopStart(), p.GetPkiEncrypted(), len(enc))
	cb, _ := proto.Marshal(&pb.Compressed{Portnum: pb.PortNum_UNKNOWN_APP, Data: enc})
	in := proto.Clone(p).(*pb.MeshPacket)
	in.PayloadVariant = &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_SIMULATOR_APP, Payload: cb}}
	in.RxRssi, in.RxSnr, in.RxTime = proto.Int32(rssi), 6.25, proto.Uint32(uint32(time.Now().Unix()))
	_ = to.Send(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: in}})
}

// injectSimulated logs a SIMULATOR_APP frame and injects it with reception details.
func injectSimulated(t *testing.T, name string, p *pb.MeshPacket, to *Client, rssi int32) {
	d := p.GetDecoded()
	var cm pb.Compressed
	_ = proto.Unmarshal(d.GetPayload(), &cm)
	t.Logf("%s air: from !%08x to !%08x id %08x ch %d hop %d/%d relay %02x next %02x pki %v inner %s (%d bytes) want_ack %v req %08x want_resp %v bitfield %v",
		name, p.GetFrom(), p.GetTo(), p.GetId(), p.GetChannel(), p.GetHopLimit(), p.GetHopStart(), p.GetRelayNode(), p.GetNextHop(),
		p.GetPkiEncrypted(), cm.GetPortnum(), len(cm.GetData()), p.GetWantAck(), d.GetRequestId(), d.GetWantResponse(), d.GetBitfield())
	in := proto.Clone(p).(*pb.MeshPacket)
	in.RxRssi, in.RxSnr, in.RxTime = proto.Int32(rssi), 6.25, proto.Uint32(uint32(time.Now().Unix()))
	if err := to.Send(&pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: in}}); err != nil {
		t.Logf("%s inject: %v", name, err)
	}
}

// sendNodeInfo broadcasts c's user, with its public key.
func sendNodeInfo(t *testing.T, c *Client, hop uint32) {
	t.Helper()
	sn := c.Snapshot()
	user := proto.Clone(sn.Self().GetUser()).(*pb.User)
	user.PublicKey = sn.Config.GetSecurity().GetPublicKey()
	ub, _ := proto.Marshal(user)
	if _, err := c.SendPacket(&pb.MeshPacket{To: 0xffffffff, HopLimit: hop,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_NODEINFO_APP, Payload: ub}}}); err != nil {
		t.Fatal(err)
	}
}

// awaitTexts waits for A's channel text to be delivered, has B send A a DM once it is, and waits
// for that too.
func awaitTexts(t *testing.T, a, b *Client, got <-chan *pb.MeshPacket, hop uint32) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	var sawText, sawDM bool
	sentDM := false
	for !(sawText && sawDM) {
		select {
		case p := <-got:
			d := p.GetDecoded()
			t.Logf("delivered to a client: from !%08x to !%08x port %s %q rssi %d snr %.2f hops %d/%d pki %v",
				p.GetFrom(), p.GetTo(), d.GetPortnum(), truncate(d.GetPayload()), p.GetRxRssi(), p.GetRxSnr(), p.GetHopLimit(), p.GetHopStart(), p.GetPkiEncrypted())
			switch textOf(d) {
			case "hello channel":
				sawText = true
				if !sentDM {
					sentDM = true
					sendDM(t, b, a.Snapshot().NodeNum(), hop)
				}
			case "hello dm":
				sawDM = true
			}
		case <-deadline:
			t.Fatalf("text %v dm %v", sawText, sawDM)
		}
	}
}

// textOf is a text message's text, or "" for other ports.
func textOf(d *pb.Data) string {
	if d.GetPortnum() != pb.PortNum_TEXT_MESSAGE_APP {
		return ""
	}
	return string(d.GetPayload())
}

// sendDM sends "hello dm" from c to node to, asking for an ack.
func sendDM(t *testing.T, c *Client, to, hop uint32) {
	t.Helper()
	if _, err := c.SendPacket(&pb.MeshPacket{To: to, WantAck: true, HopLimit: hop,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hello dm")}}}); err != nil {
		t.Fatal(err)
	}
}

func gotKind(ev <-chan Event, k EventKind, d time.Duration) bool {
	timeout := time.After(d)
	for {
		select {
		case e := <-ev:
			if e.Kind == k {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func waitKind(t *testing.T, ev <-chan Event, k EventKind, d time.Duration) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case e := <-ev:
			if e.Kind == k {
				return
			}
		case <-timeout:
			t.Fatalf("no event %d within %v", k, d)
		}
	}
}

func truncate(b []byte) string {
	if len(b) > 40 {
		b = b[:40]
	}
	return string(b)
}
