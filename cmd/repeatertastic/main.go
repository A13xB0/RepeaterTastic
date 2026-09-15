// Command repeatertastic hosts many virtual Meshtastic nodes on one LoRa modem.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/A13xB0/RepeaterTastic/internal/config"
	"github.com/A13xB0/RepeaterTastic/internal/links/mqtt"
	"github.com/A13xB0/RepeaterTastic/internal/links/udp"
	"github.com/A13xB0/RepeaterTastic/internal/logbuf"
	"github.com/A13xB0/RepeaterTastic/internal/mdns"
	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/phoneapi"
	"github.com/A13xB0/RepeaterTastic/internal/plugins"
	"github.com/A13xB0/RepeaterTastic/internal/radio"
	"github.com/A13xB0/RepeaterTastic/internal/radio/kiss"
	"github.com/A13xB0/RepeaterTastic/internal/radio/lazy"
	"github.com/A13xB0/RepeaterTastic/internal/radio/null"
	"github.com/A13xB0/RepeaterTastic/internal/site"
	"github.com/A13xB0/RepeaterTastic/internal/web"
)

var version = "dev"

// mapAPIKey is the map tile provider's API key baked in at build time
// (-ldflags "-X main.mapAPIKey=..."). REPEATERTASTIC_MAP_API_KEY overrides it.
var mapAPIKey = ""

// resolveMapAPIKey picks the tile key and says where it came from (never the key itself).
func resolveMapAPIKey() (key, source string) {
	if k := strings.TrimSpace(os.Getenv("REPEATERTASTIC_MAP_API_KEY")); k != "" {
		return k, "environment"
	}
	if mapAPIKey != "" {
		return mapAPIKey, "built in"
	}
	return "", "none"
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("repeatertastic", version)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	defaultConfig := "/etc/repeatertastic/repeatertastic.yaml"
	if p := strings.TrimSpace(os.Getenv("REPEATERTASTIC_CONFIG")); p != "" {
		defaultConfig = p
	}
	cfgPath := flag.String("config", defaultConfig, "configuration file (env REPEATERTASTIC_CONFIG)")
	flag.Parse()
	if flag.Arg(0) == "plugin" {
		os.Exit(pluginCommand(*cfgPath, flag.Args()[1:]))
	}
	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "repeatertastic:", err)
		os.Exit(1)
	}
	if restartRequested.Load() {
		os.Exit(75) // EX_TEMPFAIL: the supervisor (systemd Restart=on-failure, Docker restart policy) starts it again
	}
}

// restartRequested is set when the web GUI asks for a restart: run shuts down cleanly (saving
// identities and closing modems), then main exits 75 so the supervisor starts it again.
var restartRequested atomic.Bool

func run(cfgPath string) error {
	restoredConfig := applyStaged(cfgPath) // a backup restored from the GUI replaces the config at start
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	cfg.ApplyEnv()
	logs := logbuf.New(2000)
	level := new(slog.LevelVar) // the web GUI changes it live
	var l slog.Level
	if l.UnmarshalText([]byte(strings.ToUpper(cfg.LogLevel))) == nil {
		level.Set(l)
	}
	log := slog.New(logbuf.NewHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}), logs))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if restoredConfig {
		log.Info("configuration restored from a backup")
	}
	for _, rc := range cfg.RadioConfigs() { // ...and its identities, before any radio loads them
		if applyStaged(filepath.Join(rc.StateDir, "identities.json")) {
			log.Info("identities restored from a backup", "radio", rc.ID)
		}
	}

	rcs := cfg.RadioConfigs()
	uplinked := mqtt.NewUplinked() // one per site: a packet heard on two radios is published once
	var radios []*radioRuntime
	for _, rc := range rcs {
		rlog := log
		if len(rcs) > 1 {
			rlog = log.With("radio", rc.ID)
		}
		rt, err := startRadio(ctx, rc, rlog, uplinked)
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

	// Experimental multi-radio identities: join the radios; the switch applies live.
	hosts := make([]*mesh.Host, 0, len(radios))
	for _, rt := range radios {
		hosts = append(hosts, rt.host)
	}
	fed := mesh.NewFederation(hosts...)
	fed.SetEnabled(cfg.Experimental.MultiRadioIdentities)

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

	var pm *plugins.Manager
	if cfg.Plugins.Enabled {
		prs := make([]plugins.Radio, 0, len(radios))
		for _, rt := range radios {
			prs = append(prs, plugins.Radio{ID: rt.rc.ID, Name: rt.rc.Name, Host: rt.host})
		}
		pm, err = plugins.New(plugins.Options{Config: cfg.Plugins, Dir: cfg.PluginDir(), Radios: prs, Version: version, Log: log,
			Notify: func(id string) {
				for _, rt := range radios {
					rt.host.Bus.Publish(mesh.Event{Type: "plugin", Data: id})
				}
			}})
		if err != nil {
			return fmt.Errorf("plugins: %w", err)
		}
		if err := pm.Start(ctx); err != nil {
			return fmt.Errorf("plugins: %w", err)
		}
		defer func() { stop(); pm.Wait() }() // plugins stop before the radios close
	}

	if cfg.Web.Enabled {
		extra := make([]web.Radio, 0, len(radios)-1)
		for _, rt := range radios[1:] {
			extra = append(extra, web.Radio{ID: rt.rc.ID, Name: rt.rc.Name, Config: rt.rc.Config, Host: rt.host, API: rt.api, UDP: rt.udp, MQTT: rt.mqtt})
		}
		key, source := resolveMapAPIKey()
		log.Info("map tiles", "api_key", source)
		srv, err := web.New(web.Options{Config: cfg, Host: primary.host, API: primary.api, Logs: logs, UDP: primary.udp, MQTT: primary.mqtt,
			MapAPIKey: key, MapKeySource: source, LogLevel: level, Federation: fed, Plugins: pm,
			Restart: func() { restartRequested.Store(true); stop() },
			Radios:  extra, Site: st, Version: version, Log: log})
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
	mqtt  []*mqtt.Link
}

// startRadio opens a radio's modem, builds its host and identities and starts its client
// API and UDP link. The host itself is run by the caller.
func startRadio(ctx context.Context, rc config.RadioConfig, log *slog.Logger, uplinked *mqtt.Uplinked) (*radioRuntime, error) {
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
	origins := mqtt.NewOrigins()
	for _, mc := range rc.Links.MQTT {
		if !mc.Enabled {
			continue
		}
		l := mqtt.New(host, mqtt.Options{Name: mc.Name, Address: mc.Address, Username: mc.Username, Password: mc.Password,
			TLS: mc.TLS, Root: mc.Root, Mode: mc.Mode, Gateway: mc.Gateway, Format: mc.Format,
			UplinkChannels: mc.UplinkChannels, DownlinkChannels: mc.DownlinkChannels, ChannelSelection: mc.ChannelSelection,
			IgnoreConsent: mc.IgnoreConsent, RelayMQTT: mc.RelayMQTT, RelayHops: mc.RelayHops, CrossLink: mc.CrossLink,
			DownlinkPerMinute: mc.DownlinkPerMinute, UplinkPerMinute: mc.UplinkPerMinute, FirmwareVersion: phoneapi.FirmwareVersion,
			MapReport: mc.MapReport.Enabled, MapInterval: mc.MapReport.Interval, PositionPrecision: mc.MapReport.PositionPrecision,
			Latitude: mc.MapReport.Latitude, Longitude: mc.MapReport.Longitude, Altitude: mc.MapReport.Altitude,
			Origins: origins, Uplinked: uplinked}, log)
		rt.mqtt = append(rt.mqtt, l)
		go func() {
			if err := l.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("MQTT link stopped", "connection", mc.Name, "err", err)
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

// healthcheck is `repeatertastic healthcheck` for container health checks: the web server answers
// on 127.0.0.1 (port from REPEATERTASTIC_WEB_PORT, default 8080). Exit status 0 = healthy.
func healthcheck() int {
	port := strings.TrimSpace(os.Getenv("REPEATERTASTIC_WEB_PORT"))
	if port == "" {
		port = "8080"
	}
	client := &http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/api/v1/setup")
	if err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy: HTTP", resp.StatusCode)
		return 1
	}
	return 0
}

// applyStaged moves path+".restore" (written by a backup restore) over path. It reports whether it did.
func applyStaged(path string) bool {
	staged := path + ".restore"
	if _, err := os.Stat(staged); err != nil {
		return false
	}
	if err := os.Rename(staged, path); err != nil {
		fmt.Fprintln(os.Stderr, "repeatertastic: applying restored", path+":", err)
		return false
	}
	return true
}

// pluginCommand runs "repeatertastic plugin ...".
func pluginCommand(cfgPath string, args []string) int {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "repeatertastic:", err)
		return 1
	}
	cfg.ApplyEnv()
	if err := plugins.CLI(cfg, args, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "repeatertastic plugin:", err)
		return 1
	}
	return 0
}
