// Command kisstool is a bench tool for the radio drivers (a KISS modem, or with --board an SX126x on
// SPI): modem info, a Meshtastic listener that decodes default-key channel traffic, and a
// LongFast-style broadcast text sender.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/signal"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/ScotMesh/RepeaterTastic/internal/phy"
	"github.com/ScotMesh/RepeaterTastic/internal/radio"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/kiss"
	"github.com/ScotMesh/RepeaterTastic/internal/radio/sx126x"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
	"github.com/ScotMesh/RepeaterTastic/pb"
)

const usage = `usage: kisstool <command> [flags]

commands:
  info                     modem version, name, radio, phy extra, noise floor, stats
  listen                   configure Meshtastic PHY and print received frames
  send-text [flags] TEXT   broadcast a text message on the preset's default channel

common flags: --dev /dev/ttyUSB0 --baud 115200
  or --board /etc/meshtasticd/config.d/lora-….yaml for an SX126x on SPI (experimental)
PHY flags (listen, send-text): --region EU_868 --preset LONG_FAST --power 10
send-text flags: --from !xxxxxxxx (default random)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	dev := fs.String("dev", "/dev/ttyUSB0", "serial device")
	baud := fs.Int("baud", 115200, "baud rate")
	board := fs.String("board", "", "meshtasticd board file: use an SX126x on SPI instead of a KISS modem")
	region := fs.String("region", "EU_868", "Meshtastic region")
	preset := fs.String("preset", "LONG_FAST", "Meshtastic modem preset")
	power := fs.Int("power", 10, "TX power dBm (0 = region limit)")
	from := fs.String("from", "", "sender node number (!hex, 0x.., decimal); default random")
	// Accept flags before and after positional arguments.
	var args []string
	for rest := os.Args[2:]; ; {
		_ = fs.Parse(rest)
		if fs.NArg() == 0 {
			break
		}
		args = append(args, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var run func(context.Context, radio.Radio) error
	switch cmd {
	case "info":
		run = info
	case "listen", "send-text":
		rp, err := resolve(*region, *preset, *power)
		if err != nil {
			fatal(err)
		}
		if cmd == "listen" {
			run = func(ctx context.Context, m radio.Radio) error { return listen(ctx, m, rp) }
			break
		}
		text := strings.Join(args, " ")
		if text == "" {
			fatal(errors.New("send-text needs a message"))
		}
		node := rand.Uint32N(0xFFFFFFF0-wire.NumReserved) + wire.NumReserved
		if *from != "" {
			if node, err = wire.ParseNodeID(*from); err != nil {
				fatal(fmt.Errorf("--from: %w", err))
			}
		}
		run = func(ctx context.Context, m radio.Radio) error { return sendText(ctx, m, rp, node, text) }
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	octx, cancel := context.WithTimeout(ctx, 10*time.Second)
	m, err := open(octx, *dev, *baud, *board)
	cancel()
	if err != nil {
		fatal(err)
	}
	defer m.Close()
	if err := run(ctx, m); err != nil && !errors.Is(err, context.Canceled) {
		m.Close()
		fatal(err)
	}
}

func open(ctx context.Context, dev string, baud int, board string) (radio.Radio, error) {
	if board == "" {
		m, err := kiss.Open(ctx, kiss.Options{Device: dev, Baud: baud})
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", dev, err)
		}
		return m, nil
	}
	b, err := sx126x.LoadBoard(board)
	if err != nil {
		return nil, err
	}
	fmt.Printf("board:      %s\n", b.Summary())
	logf := func(f string, a ...any) { fmt.Printf("  "+f+"\n", a...) }
	r, err := sx126x.Open(ctx, b, logf)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", b.SPIDev, err)
	}
	return r, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "kisstool:", err)
	os.Exit(1)
}

func resolve(region, preset string, power int) (phy.RadioParams, error) {
	p, ok := pb.Config_LoRaConfig_ModemPreset_value[strings.ToUpper(preset)]
	if !ok {
		return phy.RadioParams{}, fmt.Errorf("unknown preset %q", preset)
	}
	return phy.Resolve(phy.Options{Region: region, Preset: phy.Preset(p), TxPowerDBm: power})
}

func radioConfig(rp phy.RadioParams) radio.Config {
	return radio.Config{FrequencyHz: rp.FrequencyHz(), BandwidthHz: rp.BwHz(), SF: uint8(rp.SF), CR: uint8(rp.CR),
		SyncWord: rp.SyncWord, Preamble: uint16(rp.Preamble), TxPowerDBm: int8(rp.TxPowerDBm)}
}

func configure(ctx context.Context, m radio.Radio, rp phy.RadioParams) error {
	c := radioConfig(rp)
	if err := m.Configure(ctx, c); err != nil {
		return err
	}
	fmt.Printf("configured %s %s: %.4f MHz, BW %g kHz, SF%d, CR4/%d, sync 0x%02x, preamble %d, %d dBm\n",
		rp.Region.Name, rp.PresetName(), rp.FrequencyMHz, rp.BwKHz, rp.SF, rp.CR, rp.SyncWord, rp.Preamble, rp.TxPowerDBm)
	return nil
}

func info(ctx context.Context, r radio.Radio) error {
	if s, ok := r.(*sx126x.Radio); ok {
		return spiInfo(ctx, s)
	}
	m := r.(*kiss.Modem)
	in := m.Info()
	patched := "no, flash firmware/out/Heltec_v3_kiss_modem-factory.bin"
	if m.Version() >= kiss.PatchedVersion {
		patched = "yes"
	}
	fmt.Printf("device:     %s\nname:       %s\nfirmware:   %s (sync word/preamble patch: %s)\n", in.Device, in.Name, in.Firmware, patched)
	c, err := m.ModemConfig(ctx)
	if err != nil {
		fmt.Printf("radio:      error: %v\n", err)
	} else {
		fmt.Printf("radio:      %.4f MHz, BW %g kHz, SF%d, CR4/%d, %d dBm (zeros until SetRadio)\n",
			float64(c.FrequencyHz)/1e6, float64(c.BandwidthHz)/1000, c.SF, c.CR, c.TxPowerDBm)
		fmt.Printf("phy extra:  sync 0x%02x, preamble %d\n", c.SyncWord, c.Preamble)
	}
	st := m.Stats(ctx)
	fmt.Printf("noise:      %d dBm\nstats:      rx %d, tx %d, rx errors %d\n", st.NoiseFloorDBm, st.RxPackets, st.TxPackets, st.Errors)
	return nil
}

// spiInfo reports what an SX126x on SPI says about itself. Open has already proven SPI works (the
// sync word register read back as 0x1424).
func spiInfo(ctx context.Context, r *sx126x.Radio) error {
	in := r.Info()
	fmt.Printf("device:     %s\nchip:       %s (answered on SPI: sync word register 0x1424 after reset)\n", in.Device, in.Firmware)
	status, errs, err := r.Diagnostics()
	if err != nil {
		return err
	}
	modes := map[byte]string{2: "standby RC", 3: "standby XOSC", 4: "FS", 5: "RX", 6: "TX"}
	mode, ok := modes[status>>4&7]
	if !ok {
		mode = fmt.Sprintf("unexpected mode %d", status>>4&7)
	}
	fmt.Printf("status:     0x%02x (%s)\n", status, mode)
	fmt.Printf("dev errors: 0x%04x%s\n", errs, deviceErrorNames(errs))
	// A noise floor needs the chip in RX on a real channel: configure EU_868 LongFast for a moment.
	rp, err := resolve("EU_868", "LONG_FAST", 0)
	if err != nil {
		return err
	}
	if err := configure(ctx, r, rp); err != nil {
		return fmt.Errorf("configure: %w", err)
	}
	time.Sleep(6 * time.Second)
	st := r.Stats(ctx)
	fmt.Printf("noise:      %d dBm (RX on %.4f MHz; about -100 to -125 is normal)\nstats:      rx %d, tx %d, errors %d\n",
		st.NoiseFloorDBm, rp.FrequencyMHz, st.RxPackets, st.TxPackets, st.Errors)
	return nil
}

func deviceErrorNames(e uint16) string {
	names := []string{"RC64K calibration", "RC13M calibration", "PLL calibration", "ADC calibration",
		"image calibration", "XOSC start (check the TCXO voltage)", "PLL lock", "", "PA ramp"}
	var out []string
	for i, n := range names {
		if e&(1<<i) != 0 && n != "" {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return " (none)"
	}
	return " (" + strings.Join(out, ", ") + ")"
}

func listen(ctx context.Context, m radio.Radio, rp phy.RadioParams) error {
	if err := configure(ctx, m, rp); err != nil {
		return err
	}
	hash := wire.ChannelHash(rp.PresetName(), wire.ExpandPSK([]byte{1}), false)
	fmt.Printf("listening (default channel hash 0x%02x), Ctrl-C to stop\n", hash)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case f, ok := <-m.Frames():
			if !ok {
				return nil
			}
			printFrame(os.Stdout, f, hash)
		}
	}
}

func printFrame(w io.Writer, f radio.Frame, hash uint8) {
	fmt.Fprintf(w, "\n%s  %d bytes  RSSI %d dBm  SNR %.2f dB\n  %s\n", f.At.Format("15:04:05.000"), len(f.Data), f.RSSI, f.SNR, hex.EncodeToString(f.Data))
	p := wire.DecodeFrame(f.Data, int32(f.RSSI), f.SNR)
	if p == nil {
		fmt.Fprintln(w, "  not a Meshtastic frame")
		return
	}
	fmt.Fprintf(w, "  from %s to %s id 0x%08x hop %d/%d chan 0x%02x next 0x%02x relay 0x%02x want_ack %v\n",
		wire.NodeID(p.From), wire.NodeID(p.To), p.Id, p.HopLimit, p.HopStart, p.Channel, p.NextHop, p.RelayNode, p.WantAck)
	if uint8(p.Channel) != hash {
		fmt.Fprintln(w, "  (not the default-key primary channel; PKI DM or other channel)")
		return
	}
	enc := p.GetEncrypted()
	var d pb.Data
	if err := proto.Unmarshal(wire.AESCTR(wire.DefaultPSK, p.From, p.Id, enc), &d); err != nil {
		fmt.Fprintf(w, "  decrypt/parse failed: %v\n", err)
		return
	}
	fmt.Fprintf(w, "  port %s, %d byte payload", d.Portnum, len(d.Payload))
	if d.Portnum == pb.PortNum_TEXT_MESSAGE_APP {
		fmt.Fprintf(w, ": %q", d.Payload)
	}
	fmt.Fprintln(w)
}

func sendText(ctx context.Context, m radio.Radio, rp phy.RadioParams, from uint32, text string) error {
	if err := configure(ctx, m, rp); err != nil {
		return err
	}
	id := rand.Uint32() | 1
	frame, err := buildText(rp, from, id, text)
	if err != nil {
		return err
	}
	fmt.Printf("sending %d bytes from %s id 0x%08x (airtime ~%.0f ms)\n  %s\n", len(frame), wire.NodeID(from), id, rp.AirtimeMs(len(frame)), hex.EncodeToString(frame))
	start := time.Now()
	if err := m.Send(ctx, frame); err != nil {
		return err
	}
	fmt.Printf("TxDone after %v\n", time.Since(start).Round(time.Millisecond))
	return nil
}

// buildText makes a broadcast TEXT_MESSAGE_APP frame on the preset's default (AQ==) channel.
func buildText(rp phy.RadioParams, from, id uint32, text string) ([]byte, error) {
	one := uint32(1)
	plain, err := proto.Marshal(&pb.Data{Portnum: pb.PortNum_TEXT_MESSAGE_APP, Payload: []byte(text), Bitfield: &one})
	if err != nil {
		return nil, err
	}
	p := &pb.MeshPacket{
		From:      from,
		To:        wire.Broadcast,
		Id:        id,
		Channel:   uint32(wire.ChannelHash(rp.PresetName(), wire.ExpandPSK([]byte{1}), false)),
		HopLimit:  3,
		HopStart:  3,
		RelayNode: uint32(wire.LastByte(from)),
	}
	p.PayloadVariant = &pb.MeshPacket_Encrypted{Encrypted: wire.AESCTR(wire.DefaultPSK, from, p.Id, plain)}
	return wire.EncodeFrame(p)
}
