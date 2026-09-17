package mesh

import (
	"testing"
	"time"

	pb "github.com/ScotMesh/RepeaterTastic/api/meshtastic"
)

type sink struct{ ch chan *pb.FromRadio }

func newSink() *sink                           { return &sink{ch: make(chan *pb.FromRadio, 256)} }
func (s *sink) SendFromRadio(fr *pb.FromRadio) { s.ch <- fr }

// waitPacket returns the first delivered packet matching fn.
func (s *sink) waitPacket(t *testing.T, d time.Duration, fn func(*pb.MeshPacket) bool) *pb.MeshPacket {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case fr := <-s.ch:
			if p := fr.GetPacket(); p != nil && fn(p) {
				return p
			}
		case <-deadline:
			t.Fatalf("timed out waiting for packet")
			return nil
		}
	}
}

func text(p *pb.MeshPacket) string {
	if d := p.GetDecoded(); d != nil && d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
		return string(d.Payload)
	}
	return ""
}
