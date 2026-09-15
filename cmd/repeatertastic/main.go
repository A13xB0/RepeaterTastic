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
	"github.com/A13xB0/RepeaterTastic/internal/links/mqtt"
	"github.com/A13xB0/RepeaterTastic/internal/links/udp"
	"github.com/A13xB0/RepeaterTastic/internal/logbuf"
	"github.com/A13xB0/RepeaterTastic/internal/mdns"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/phoneapi"
	"github.com/A13xB0/RepeaterTastic/internal/radio"
	"github.com/A13xB0/RepeaterTastic/internal/radio/kiss"
	"github.com/A13xB0/RepeaterTastic/internal/radio/lazy"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
	"github.com/A13xB0/RepeaterTastic/internal/site"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rcs := cfg.RadioConfigs()
	var radios []*radioRuntime
	for _, rc := range rcs {
		rlog := log
		if len(rcs) > 1 {
			rlog = log.With("radio", rc.ID)
		}
		rt, err := startRadio(ctx, rc, rlog)
		if err != nil {
			return fmt.Errorf("radio %s: %w", rc.ID, err)
		}
		defer rt.radio.Close()
		radios = append(radios, rt)
	}
	primary := radios[0]
	logs.OnEntry(func(e logbuf.Entry) {
		for _, rt := range radios {
			rt.host.Bus.Publish(mesh.Event{Type: "log", Data: e})
		}
	})

	// One site coordinator whenever several radios share a mast (co-channel transmit
	// turns) or a site-wide airtime budget is set.
	var st *site.Site
	if len(radios) > 1 || cfg.Site.DutyCyclePct > 0 {
		st = site.New(cfg.Site.DutyCyclePct)
		for _, rt := range radios {
			st.Add(rt.host)
		}
		for _, rt := range radios {
			if o := st.Overlaps(rt.host); len(o) > 0 {
				ids := make([]string, 0, len(o))
				for _, h := range o {
					ids = append(ids, h.RadioID())
				}
				log.Warn("radio shares its channel with other radios on this site; they will take turns to transmit",
					"radio", rt.rc.ID, "overlaps", strings.Join(ids, ","))
			}
		}
	}

	if cfg.MDNS.Enabled {
		hosts := make([]*mesh.Host, 0, len(radios))
		for _, rt := range radios {
			hosts = append(hosts, rt.host)
		}
		go runMDNS(ctx, hosts, log)
	}

	if cfg.Web.Enabled {
		extra := make([]web.Radio, 0, len(radios)-1)
		for _, rt := range radios[1:] {
			extra = append(extra, web.Radio{ID: rt.rc.ID, Name: rt.rc.Name, Config: rt.rc.Config, Host: rt.host, API: rt.api, UDP: rt.udp, MQTT: rt.mqtt})
		}
		srv, err := web.New(web.Options{Config: cfg, Host: primary.host, API: primary.api, Logs: logs, UDP: primary.udp, MQTT: primary.mqtt,
			Radios: extra, Site: st, Version: version, Log: log})
		if err != nil {
			return err
		}
		go func() {
			if err := srv.Run(ctx); err != nil {
				log.Error("web server stopped", "err", err)
			}
		}()
	}

	for _, rt := range radios {
		rp := rt.host.RadioParams()
		log.Info("RepeaterTastic starting", "version", version, "radio", rt.rc.ID, "region", rp.Region.Name, "preset", rp.PresetName(),
			"freq_mhz", rp.FrequencyMHz, "identities", len(rt.host.Identities()))
	}
	errs := make(chan error, len(radios))
	for _, rt := range radios[1:] {
		go func() { errs <- rt.host.Run(ctx) }()
	}
	err = primary.host.Run(ctx)
	stop() // one radio stopping stops the others
	for range radios[1:] {
		if e := <-errs; err == nil || errors.Is(err, context.Canceled) {
			err = e
		}
	}
	for _, rt := range radios {
		_ = rt.host.SaveIdentities()
	}
	if err == nil || errors.Is(err, context.Canceled) {
		log.Info("stopped")
		return nil
	}
	return err
}

// radioRuntime is one radio's running stack.
type radioRuntime struct {
	rc    config.RadioConfig
	radio radio.Radio
	host  *mesh.Host
	api   *phoneapi.Manager
	udp   *udp.Link
	mqtt  *mqtt.Link
}

// startRadio opens a radio's modem, builds its host and identities and starts its client
// API and UDP link. The host itself is run by the caller.
func startRadio(ctx context.Context, rc config.RadioConfig, log *slog.Logger) (*radioRuntime, error) {
	if err := os.MkdirAll(rc.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	var r radio.Radio
	switch rc.Radio.Driver {
	case "kiss":
		logf := func(f string, a ...any) { log.Info(fmt.Sprintf(f, a...), "radio", "kiss") }
		opts := kiss.Options{Device: rc.Radio.Device, Baud: rc.Radio.Baud, Logf: logf}
		r = lazy.New(func(ctx context.Context) (radio.Radio, error) { return kiss.Open(ctx, opts) },
			radio.Info{Driver: "kiss", Device: rc.Radio.Device}, 5*time.Second, logf)
	default:
		r = null.New()
	}

	host, err := mesh.NewHost(rc.MeshConfig(), r, log)
	if err != nil {
		r.Close()
		return nil, err
	}
	if err := loadIdentities(rc.Config, host, log); err != nil {
		r.Close()
		return nil, err
	}
	api := phoneapi.NewManager(host, log)
	go api.Run(ctx)

	rt := &radioRuntime{rc: rc, radio: r, host: host, api: api}
	if rc.Links.UDPMulticast.Enabled {
		var groups []string
		if g := rc.Links.UDPMulticast.Group; g != "" && !strings.Contains(g, ":") {
			groups = strings.Split(g, ",")
		}
		rt.udp, err = udp.New(host, groups, 0, "", log)
		if err != nil {
			r.Close()
			return nil, err
		}
		go func() {
			if err := rt.udp.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("UDP link stopped", "err", err)
			}
		}()
	}
	if mc := rc.Links.MQTT; mc.Enabled {
		rt.mqtt = mqtt.New(host, mqtt.Options{Address: mc.Address, Username: mc.Username, Password: mc.Password, TLS: mc.TLS,
			Root: mc.Root, DownlinkPerMinute: mc.DownlinkPerMinute, FirmwareVersion: phoneapi.FirmwareVersion,
			MapReport: mc.MapReport.Enabled, MapInterval: mc.MapReport.Interval, PositionPrecision: mc.MapReport.PositionPrecision,
			Latitude: mc.MapReport.Latitude, Longitude: mc.MapReport.Longitude, Altitude: mc.MapReport.Altitude}, log)
		go func() {
			if err := rt.mqtt.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("MQTT link stopped", "err", err)
			}
		}()
	}
	return rt, nil
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

// runMDNS keeps the advertised _meshtastic._tcp services in step with every radio's identities.
func runMDNS(ctx context.Context, hosts []*mesh.Host, log *slog.Logger) {
	r := mdns.New(log)
	update := func() {
		var svcs []mdns.Service
		for _, host := range hosts {
			for _, id := range host.Identities() {
				if id.IsRelay || !id.Enabled || id.APIPort <= 0 {
					continue
				}
				u := id.UserCopy()
				svcs = append(svcs, mdns.Service{Instance: fmt.Sprintf("%s (%s)", u.LongName, id.NodeID()), Port: id.APIPort,
					TXT: map[string]string{"id": id.NodeID(), "shortname": u.ShortName, "pio_env": "repeatertastic"}})
			}
		}
		r.SetServices(svcs)
	}
	update()
	for _, host := range hosts {
		events, unsub := host.Bus.Subscribe(16)
		defer unsub()
		go func() {
			for e := range events {
				if e.Type == "identity" {
					update()
				}
			}
		}()
	}
	if err := r.Run(ctx); err != nil {
		log.Warn("mDNS advertising disabled", "err", err)
	}
}
