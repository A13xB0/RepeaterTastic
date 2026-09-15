// Command hello is an example RepeaterTastic plugin: it counts packets per radio, keeps a list
// of the nodes it has seen for its panel, and can answer "ping" sent to a relay persona.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/pb"
	pluginv1 "github.com/ScotMesh/RepeaterTastic/pluginapi/v1"
	"github.com/ScotMesh/RepeaterTastic/pluginsdk"
)

var version = "1.0.0"

type settings struct {
	ReplyToPing bool   `json:"reply_to_ping"`
	ReplyText   string `json:"reply_text"`
}

type seen struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Radio    string  `json:"radio"`
	Packets  int     `json:"packets"`
	LastSeen int64   `json:"last_seen"`
	SNR      float32 `json:"snr"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	c, err := pluginsdk.Connect(ctx, pluginsdk.Options{Version: version})
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	var cfg settings
	_ = c.Settings(&cfg)
	perRadio := map[string]int{}
	nodes := map[uint32]*seen{}
	for _, r := range c.Welcome.Radios {
		perRadio[r.Id] = 0
	}
	_ = c.Log("info", "hello from %s, watching %d radio(s)", version, len(c.Welcome.Radios))

	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	report := func() {
		total := 0
		fields := map[string]string{}
		for id, n := range perRadio {
			total += n
			fields["Packets on "+id] = fmt.Sprint(n)
		}
		fields["Nodes seen"] = fmt.Sprint(len(nodes))
		_ = c.Status(fmt.Sprintf("%d packets from %d nodes", total, len(nodes)), "ok", fields)
		list := make([]*seen, 0, len(nodes))
		for _, n := range nodes {
			list = append(list, n)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].LastSeen > list[j].LastSeen })
		if len(list) > 50 {
			list = list[:50]
		}
		_ = c.Panel(map[string]any{"radios": perRadio, "nodes": list, "updated": time.Now().UnixMilli()})
	}
	report()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			report()
		case msg, ok := <-c.Events():
			if !ok {
				if err := c.Err(); err != nil {
					log.Printf("session ended: %v", err)
				}
				return
			}
			switch {
			case msg.GetPacket() != nil:
				ev := msg.GetPacket()
				if ev.Direction != "rx" {
					continue
				}
				perRadio[ev.RadioId]++
				p := &pb.MeshPacket{}
				if proto.Unmarshal(ev.MeshPacket, p) != nil {
					continue
				}
				n := nodes[p.From]
				if n == nil && len(nodes) >= 500 {
					continue // keep the example's memory bounded
				}
				if n == nil {
					n = &seen{ID: fmt.Sprintf("!%08x", p.From)}
					nodes[p.From] = n
				}
				n.Radio, n.Packets, n.LastSeen, n.SNR = ev.RadioId, n.Packets+1, ev.TimeMs, p.RxSnr
			case msg.GetNode() != nil:
				nd := msg.GetNode().Node
				u := &pb.User{}
				if len(nd.User) > 0 && proto.Unmarshal(nd.User, u) == nil {
					if n := nodes[nd.NodeNum]; n != nil {
						n.Name = u.LongName
					}
				}
			case msg.GetText() != nil:
				answer(c, cfg, msg.GetText())
			case msg.GetSettings() != nil:
				_ = c.Settings(&cfg)
				_ = c.Log("info", "settings changed: answer ping %v", cfg.ReplyToPing)
			case msg.GetAction() != nil:
				if msg.GetAction().Name == "clear" {
					nodes = map[uint32]*seen{}
					for id := range perRadio {
						perRadio[id] = 0
					}
					_ = c.Log("info", "counters cleared from the panel")
					report()
				}
			case msg.GetStop() != nil:
				return
			}
		}
	}
}

func answer(c *pluginsdk.Client, cfg settings, t *pluginv1.TextMessageEvent) {
	if !cfg.ReplyToPing || t.Direction != "in" || !strings.EqualFold(strings.TrimSpace(t.Text), "ping") {
		return
	}
	reply := cfg.ReplyText
	if reply == "" {
		reply = "pong"
	}
	req := &pluginv1.SendTextRequest{RadioId: t.RadioId, Channel: t.Channel, Text: reply}
	if t.Direct {
		req.To, req.Channel = fmt.Sprintf("!%08x", t.From), 0
	}
	if _, err := c.Host.SendText(c.Context(), req); err != nil {
		_ = c.Log("warn", "couldn't answer ping: %v", err)
	}
}
