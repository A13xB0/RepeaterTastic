// Command repeatertastic hosts many virtual Meshtastic nodes on one LoRa modem.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/links/udp"
	"github.com/A13xB0/RepeaterTastic/internal/logbuf"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/phoneapi"
	"github.com/A13xB0/RepeaterTastic/internal/radio"
	"github.com/A13xB0/RepeaterTastic/internal/radio/kiss"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
	"github.com/A13xB0/RepeaterTastic/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("repeatertastic", version)
		return
	}
	cfgPath := flag.String("config", "/etc/repeatertastic/repeatertastic.yaml", "configuration file")
	flag.Parse()
	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "repeatertastic:", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	logs := logbuf.New(2000)
	level := slog.LevelInfo
	_ = level.UnmarshalText([]byte(strings.ToUpper(cfg.LogLevel)))
	log := slog.New(logbuf.NewHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}), logs))
	slog.SetDefault(log)
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return fmt.Errorf("state dir: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var r radio.Radio
	switch cfg.Radio.Driver {
	case "kiss":
		m, err := kiss.Open(ctx, kiss.Options{Device: cfg.Radio.Device, Baud: cfg.Radio.Baud, Logf: func(f string, a ...any) { log.Info(fmt.Sprintf(f, a...), "radio", "kiss") }})
		if err != nil {
			return fmt.Errorf("opening modem %s: %w", cfg.Radio.Device, err)
		}
		r = m
	case "none", "sim":
		r = null.New()
	}
	defer r.Close()

	host, err := mesh.NewHost(cfg.MeshConfig(), r, log)
	if err != nil {
		return err
	}
	if err := loadIdentities(cfg, host, log); err != nil {
		return err
	}
	logs.OnEntry(func(e logbuf.Entry) { host.Bus.Publish(mesh.Event{Type: "log", Data: e}) })

	api := phoneapi.NewManager(host, log)
	go api.Run(ctx)

	var udpLink *udp.Link
	if cfg.Links.UDPMulticast.Enabled {
		var groups []string
		if g := cfg.Links.UDPMulticast.Group; g != "" && !strings.Contains(g, ":") {
			groups = strings.Split(g, ",")
		}
		udpLink, err = udp.New(host, groups, 0, "", log)
		if err != nil {
			return err
		}
		go func() {
			if err := udpLink.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("UDP link stopped", "err", err)
			}
		}()
	}

	if cfg.Web.Enabled {
		srv, err := web.New(web.Options{Config: cfg, Host: host, API: api, Logs: logs, UDP: udpLink, Version: version, Log: log})
		if err != nil {
			return err
		}
		go func() {
			if err := srv.Run(ctx); err != nil {
				log.Error("web server stopped", "err", err)
			}
		}()
	}

	rp := host.RadioParams()
	log.Info("RepeaterTastic starting", "version", version, "region", rp.Region.Name, "preset", rp.PresetName(),
		"freq_mhz", rp.FrequencyMHz, "identities", len(host.Identities()))
	err = host.Run(ctx)
	_ = host.SaveIdentities()
	if errors.Is(err, context.Canceled) {
		log.Info("stopped")
		return nil
	}
	return err
}

// loadIdentities restores identities from the state dir, or creates the relay persona and the
// identities listed in the config on first start.
func loadIdentities(cfg *config.Config, host *mesh.Host, log *slog.Logger) error {
	recs, err := mesh.LoadIdentityRecords(cfg.StateDir)
	if err == nil && len(recs) > 0 {
		for _, rec := range recs {
			id, err := mesh.IdentityFromRecord(rec)
			if err != nil {
				return err
			}
			if err := host.AddIdentity(id); err != nil {
				return err
			}
		}
		if host.Relay() != nil {
			return nil
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading identities: %w", err)
	}

	if host.Relay() == nil {
		relay, err := newUniqueIdentity(host, cfg.Relay.LongName, cfg.Relay.ShortName)
		if err != nil {
			return err
		}
		relay.IsRelay = true
		if err := host.AddIdentity(relay); err != nil {
			return err
		}
		log.Info("created relay persona", "node", relay.NodeID())
	}
	if len(recs) == 0 {
		for _, ci := range cfg.Identities {
			id, err := newUniqueIdentity(host, ci.LongName, ci.ShortName)
			if err != nil {
				return err
			}
			id.APIPort, id.APIBind = ci.APIPort, ci.APIBind
			if err := host.AddIdentity(id); err != nil {
				return err
			}
			log.Info("created identity", "node", id.NodeID(), "name", ci.LongName, "api_port", ci.APIPort)
		}
	}
	return host.SaveIdentities()
}

// newUniqueIdentity generates keys until the node's last byte is free locally and among known nodes.
func newUniqueIdentity(host *mesh.Host, long, short string) (*mesh.Identity, error) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		id, err := mesh.NewIdentity(nil, long, short)
		if err != nil {
			continue
		}
		if host.DB.LastByteCollision(id.NodeNum) == 0 {
			return id, nil
		}
	}
	return nil, errors.New("couldn't find a free node number")
}
