package mqtt

import (
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/eclipse/paho.mqtt.golang/packets"
)

// fakeBroker is a minimal in-process MQTT 3.1.1 broker on 127.0.0.1: CONNECT, QoS 0 PUBLISH with
// +/# topic routing, SUBSCRIBE, UNSUBSCRIBE, PINGREQ and DISCONNECT. Enough for paho clients.
type fakeBroker struct {
	ln net.Listener

	mu       sync.Mutex
	conns    map[*brokerConn]bool
	connects int
}

type brokerConn struct {
	c    net.Conn
	wmu  sync.Mutex
	subs map[string]bool // under fakeBroker.mu
}

func (bc *brokerConn) write(p packets.ControlPacket) {
	bc.wmu.Lock()
	defer bc.wmu.Unlock()
	_ = p.Write(bc.c)
}

// startFakeBroker listens on a free local port until the test ends.
func startFakeBroker(t *testing.T) *fakeBroker {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b := &fakeBroker{ln: ln, conns: map[*brokerConn]bool{}}
	t.Cleanup(func() {
		_ = ln.Close()
		b.dropAll()
	})
	go b.accept()
	return b
}

func (b *fakeBroker) addr() string { return b.ln.Addr().String() }

// connections is how many CONNECTs the broker has accepted.
func (b *fakeBroker) connections() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.connects
}

func (b *fakeBroker) accept() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		bc := &brokerConn{c: c, subs: map[string]bool{}}
		b.mu.Lock()
		b.conns[bc] = true
		b.mu.Unlock()
		go b.serve(bc)
	}
}

// dropAll closes every client connection, as a broker restart would.
func (b *fakeBroker) dropAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for bc := range b.conns {
		_ = bc.c.Close()
		delete(b.conns, bc)
	}
}

func (b *fakeBroker) serve(bc *brokerConn) {
	defer func() {
		_ = bc.c.Close()
		b.mu.Lock()
		delete(b.conns, bc)
		b.mu.Unlock()
	}()
	for {
		cp, err := packets.ReadPacket(bc.c)
		if err != nil || !b.handle(bc, cp) {
			return
		}
	}
}

// handle answers one packet; false ends the connection.
func (b *fakeBroker) handle(bc *brokerConn, cp packets.ControlPacket) bool {
	switch p := cp.(type) {
	case *packets.ConnectPacket:
		b.mu.Lock()
		b.connects++
		b.mu.Unlock()
		bc.write(packets.NewControlPacket(packets.Connack))
	case *packets.SubscribePacket:
		b.setSubs(bc, p.Topics, true)
		ack := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
		ack.MessageID, ack.ReturnCodes = p.MessageID, make([]byte, len(p.Topics))
		bc.write(ack)
	case *packets.UnsubscribePacket:
		b.setSubs(bc, p.Topics, false)
		ack := packets.NewControlPacket(packets.Unsuback).(*packets.UnsubackPacket)
		ack.MessageID = p.MessageID
		bc.write(ack)
	case *packets.PublishPacket:
		b.route(p.TopicName, p.Payload)
	case *packets.PingreqPacket:
		bc.write(packets.NewControlPacket(packets.Pingresp))
	default: // DISCONNECT, or QoS > 0 which this broker doesn't do
		return false
	}
	return true
}

func (b *fakeBroker) setSubs(bc *brokerConn, topics []string, on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range topics {
		if on {
			bc.subs[t] = true
		} else {
			delete(bc.subs, t)
		}
	}
}

// route delivers a publish to every connection with a matching subscription.
func (b *fakeBroker) route(topic string, payload []byte) {
	var to []*brokerConn
	b.mu.Lock()
	for bc := range b.conns {
		for f := range bc.subs {
			if topicMatch(f, topic) {
				to = append(to, bc)
				break
			}
		}
	}
	b.mu.Unlock()
	for _, bc := range to {
		p := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
		p.TopicName, p.Payload = topic, payload
		bc.write(p)
	}
}

// topicMatch reports whether an MQTT topic filter (with + and #) matches topic.
func topicMatch(filter, topic string) bool {
	fs, ts := strings.Split(filter, "/"), strings.Split(topic, "/")
	for i, f := range fs {
		if f == "#" {
			return true
		}
		if i >= len(ts) || (f != "+" && f != ts[i]) {
			return false
		}
	}
	return len(fs) == len(ts)
}

func TestTopicMatch(t *testing.T) {
	for _, c := range []struct {
		filter, topic string
		want          bool
	}{
		{"msh/#", "msh/EU_868/2/e/LongFast/!1", true},
		{"msh/+/2/e/LongFast/+", "msh/EU_868/2/e/LongFast/!1", true},
		{"msh/+/2/e/LongFast/+", "msh/EU_868/2/e/Other/!1", false},
		{"a/b", "a/b/c", false},
		{"a/b/c", "a/b", false},
	} {
		if got := topicMatch(c.filter, c.topic); got != c.want {
			t.Errorf("topicMatch(%q, %q) = %v", c.filter, c.topic, got)
		}
	}
}
