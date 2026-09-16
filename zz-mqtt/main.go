package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

// Watches the RTTest topics and, with "inject", publishes one encrypted envelope from a fake node.
func main() {
	var ch struct{ Name, Psk string }
	b, _ := os.ReadFile("../rttest-channel.json")
	_ = json.Unmarshal(b, &ch)
	psk, _ := base64.StdEncoding.DecodeString(ch.Psk)
	key := wire.ExpandPSK(psk)
	hash := wire.ChannelHash(ch.Name, key, false)
	c := mqtt.NewClient(mqtt.NewClientOptions().AddBroker("tcp://127.0.0.1:18883").SetClientID(fmt.Sprintf("rt-check-%d", time.Now().UnixNano())))
	if t := c.Connect(); t.Wait() && t.Error() != nil {
		panic(t.Error())
	}
	c.Subscribe("msh/EU_868/RTTEST/#", 0, func(_ mqtt.Client, m mqtt.Message) {
		var env pb.ServiceEnvelope
		if proto.Unmarshal(m.Payload(), &env) != nil {
			fmt.Println("msg (not an envelope)", m.Topic())
			return
		}
		p := env.GetPacket()
		text := ""
		if enc := p.GetEncrypted(); enc != nil {
			var d pb.Data
			if proto.Unmarshal(wire.AESCTR(key, p.From, p.Id, enc), &d) == nil {
				text = string(d.Payload)
			}
		}
		fmt.Printf("%s %s gw=%s from=%s id=%08x hl=%d hs=%d via_mqtt=%v %q\n", time.Now().Format("15:04:05"), m.Topic(), env.GatewayId, wire.NodeID(p.From), p.Id, p.HopLimit, p.HopStart, p.ViaMqtt, text)
	}).Wait()
	if len(os.Args) > 1 && os.Args[1] == "inject" {
		time.Sleep(time.Second)
		const from = 0x0dd0beef
		id := uint32(time.Now().Unix())
		data, _ := proto.Marshal(&pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte("T12 via the broker only"), Bitfield: proto.Uint32(1)})
		p := &pb.MeshPacket{From: from, To: wire.Broadcast, Id: id, Channel: uint32(hash), HopLimit: 3, HopStart: 3,
			PayloadVariant: &pb.MeshPacket_Encrypted{Encrypted: wire.AESCTR(key, from, id, data)}}
		payload, _ := proto.Marshal(&pb.ServiceEnvelope{Packet: p, ChannelId: ch.Name, GatewayId: "!0dd0beef"})
		c.Publish("msh/EU_868/RTTEST/2/e/RTTest/!0dd0beef", 0, false, payload).Wait()
		fmt.Printf("injected id=%08x\n", id)
	}
	time.Sleep(60 * time.Second)
}
