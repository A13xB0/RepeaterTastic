package phoneapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
	"github.com/A13xB0/RepeaterTastic/pb"
)

func testServer(t *testing.T) (*mesh.Host, *mesh.Identity, *Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := mesh.NewHost(mesh.Config{Region: "EU_868", Preset: pb.Config_LoRaConfig_LONG_FAST}, null.New(), log)
	if err != nil {
		t.Fatal(err)
	}
	var relay, id *mesh.Identity
	for relay == nil || id == nil {
		r, _ := mesh.NewIdentity(nil, "Relay", "RLY")
		i, _ := mesh.NewIdentity(nil, "Base Camp", "BASE")
		if r == nil || i == nil || wire.LastByte(r.NodeNum) == wire.LastByte(i.NodeNum) {
			continue
		}
		r.IsRelay = true
		relay, id = r, i
	}
	if err := h.AddIdentity(relay); err != nil {
		t.Fatal(err)
	}
	if err := h.AddIdentity(id); err != nil {
		t.Fatal(err)
	}
	go func() { _ = h.Run(ctx) }()
	srv, err := Listen(ctx, h, id, "127.0.0.1:0", log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return h, id, srv
}

func writeToRadio(t *testing.T, c net.Conn, tr *pb.ToRadio) {
	t.Helper()
	b, _ := proto.Marshal(tr)
	hdr := []byte{start1, start2, byte(len(b) >> 8), byte(len(b))}
	if _, err := c.Write(append(hdr, b...)); err != nil {
		t.Fatal(err)
	}
}

func readFromRadio(t *testing.T, c net.Conn) *pb.FromRadio {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		t.Fatal(err)
	}
	if hdr[0] != start1 || hdr[1] != start2 {
		t.Fatalf("bad frame start %x", hdr)
	}
	b := make([]byte, binary.BigEndian.Uint16(hdr[2:]))
	if _, err := io.ReadFull(c, b); err != nil {
		t.Fatal(err)
	}
	fr := &pb.FromRadio{}
	if err := proto.Unmarshal(b, fr); err != nil {
		t.Fatal(err)
	}
	return fr
}

func TestStreamHandshakeAdminAndSend(t *testing.T) {
	_, id, srv := testServer(t)
	c, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Clients often print debug text before framing; the reader must skip it.
	_, _ = c.Write([]byte("hello\n"))
	writeToRadio(t, c, &pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 1234}})

	first := readFromRadio(t, c)
	if first.GetMyInfo().GetMyNodeNum() != id.NodeNum {
		t.Fatalf("first frame should be my_info for %08x, got %v", id.NodeNum, first)
	}
	var channels, configs, modules int
	var sawMeta, sawOwn bool
	for {
		fr := readFromRadio(t, c)
		switch {
		case fr.GetChannel() != nil:
			channels++
		case fr.GetConfig() != nil:
			configs++
		case fr.GetModuleConfig() != nil:
			modules++
		case fr.GetMetadata() != nil:
			sawMeta = true
		case fr.GetNodeInfo().GetNum() == id.NodeNum:
			sawOwn = fr.GetNodeInfo().GetUser().GetPublicKey() != nil
		}
		if fr.GetConfigCompleteId() != 0 {
			if fr.GetConfigCompleteId() != 1234 {
				t.Fatalf("config_complete_id %d", fr.GetConfigCompleteId())
			}
			break
		}
	}
	if channels != 8 || configs < 8 || modules < 10 || !sawMeta || !sawOwn {
		t.Fatalf("handshake incomplete: channels=%d configs=%d modules=%d meta=%v own=%v", channels, configs, modules, sawMeta, sawOwn)
	}

	// Admin get_owner to ourselves.
	adm, _ := proto.Marshal(&pb.AdminMessage{PayloadVariant: &pb.AdminMessage_GetOwnerRequest{GetOwnerRequest: true}})
	writeToRadio(t, c, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: &pb.MeshPacket{To: id.NodeNum, Id: 77,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_ADMIN_APP, Payload: adm, WantResponse: true}}}}})
	var owner *pb.User
	for owner == nil {
		fr := readFromRadio(t, c)
		if p := fr.GetPacket(); p != nil && p.GetDecoded().GetPortnum() == pb.PortNum_ADMIN_APP && p.GetDecoded().RequestId == 77 {
			resp := &pb.AdminMessage{}
			_ = proto.Unmarshal(p.GetDecoded().Payload, resp)
			owner = resp.GetGetOwnerResponse()
			if len(resp.SessionPasskey) != 8 {
				t.Fatal("admin response without session passkey")
			}
		}
	}
	if owner.LongName != "Base Camp" {
		t.Fatalf("owner %v", owner)
	}

	// A broadcast text gets a QueueStatus for its id.
	writeToRadio(t, c, &pb.ToRadio{PayloadVariant: &pb.ToRadio_Packet{Packet: &pb.MeshPacket{To: wire.Broadcast, Id: 4242,
		PayloadVariant: &pb.MeshPacket_Decoded{Decoded: &pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("hi")}}}}})
	for {
		fr := readFromRadio(t, c)
		if qs := fr.GetQueueStatus(); qs != nil && qs.MeshPacketId == 4242 {
			break
		}
	}
}

func TestHTTPClientAPI(t *testing.T) {
	_, id, srv := testServer(t)
	base := "http://" + srv.Addr().String()
	b, _ := proto.Marshal(&pb.ToRadio{PayloadVariant: &pb.ToRadio_WantConfigId{WantConfigId: 99}})
	req, _ := http.NewRequest(http.MethodPut, base+"/api/v1/toradio", bytes.NewReader(b))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("toradio: %v %v", err, resp)
	}
	resp.Body.Close()
	var gotMyInfo, complete bool
	for i := 0; i < 200 && !complete; i++ {
		resp, err := http.Get(base + "/api/v1/fromradio")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if len(body) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		fr := &pb.FromRadio{}
		if err := proto.Unmarshal(body, fr); err != nil {
			t.Fatal(err)
		}
		if fr.GetMyInfo().GetMyNodeNum() == id.NodeNum {
			gotMyInfo = true
		}
		complete = fr.GetConfigCompleteId() == 99
	}
	if !gotMyInfo || !complete {
		t.Fatalf("http handshake myinfo=%v complete=%v", gotMyInfo, complete)
	}
}
