package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/A13xB0/RepeaterTastic/internal/mesh"
	"github.com/A13xB0/RepeaterTastic/internal/wire"
	pluginv1 "github.com/A13xB0/RepeaterTastic/pluginapi/v1"
)

// session is one connected plugin's event stream.
type session struct {
	token   string
	out     chan *pluginv1.HostMessage
	done    chan struct{}
	once    sync.Once
	reason  atomic.Value // string
	dropped atomic.Uint64
}

func (s *session) send(msg *pluginv1.HostMessage) bool {
	select {
	case <-s.done:
		return false
	default:
	}
	select {
	case s.out <- msg:
		return true
	default:
		s.dropped.Add(1)
		return false
	}
}

// stop asks the plugin to stop; it should close the stream and exit.
func (s *session) stop(reason string) {
	s.send(&pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Stop{Stop: &pluginv1.Stop{Reason: reason}}})
}

// close ends the stream from our side.
func (s *session) close(reason string) {
	s.once.Do(func() {
		s.reason.Store(reason)
		close(s.done)
	})
}

type ctxKey struct{}

// serve listens on the Unix socket (and TCP, if configured) and serves the Plugin API.
func (m *Manager) serve(ctx context.Context) (*grpc.Server, error) {
	auth := func(ctx context.Context) (context.Context, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		var tok string
		if v := md.Get("authorization"); len(v) > 0 {
			tok = strings.TrimPrefix(v[0], "Bearer ")
		}
		if tok == "" {
			return nil, status.Error(codes.Unauthenticated, "missing plugin token")
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		for _, p := range m.plugins {
			if (p.rec.Attached && tokenMatches(hashToken(tok), p.rec.TokenHash)) || (!p.rec.Attached && tokenMatches(tok, p.token)) {
				return context.WithValue(ctx, ctxKey{}, p), nil
			}
		}
		return nil, status.Error(codes.Unauthenticated, "unknown plugin token")
	}
	// A bug in a handler must not take the daemon (and its radios) down with it.
	recovered := func(err *error) {
		if v := recover(); v != nil {
			m.log.Error("plugin API handler panicked", "panic", v)
			*err = status.Error(codes.Internal, "internal error")
		}
	}
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (resp any, err error) {
			defer recovered(&err)
			ctx, err = auth(ctx)
			if err != nil {
				return nil, err
			}
			return h(ctx, req)
		}),
		grpc.StreamInterceptor(func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) (err error) {
			defer recovered(&err)
			ctx, err := auth(ss.Context())
			if err != nil {
				return err
			}
			return h(srv, &authedStream{ServerStream: ss, ctx: ctx})
		}),
		// Notice attached plugins whose connection died without closing (a half-open TCP link).
		grpc.KeepaliveParams(keepalive.ServerParameters{Time: 30 * time.Second, Timeout: 10 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 10 * time.Second, PermitWithoutStream: true}),
	)
	pluginv1.RegisterPluginHostServer(srv, &hostServer{m: m})

	sock := m.socketPath()
	_ = os.Remove(sock)
	ul, err := net.Listen("unix", sock)
	if err != nil {
		return nil, fmt.Errorf("plugin socket: %w", err)
	}
	_ = os.Chmod(sock, 0o600)
	go func() { _ = srv.Serve(ul) }()
	if addr := m.opt.Config.Listen; addr != "" {
		tl, err := net.Listen("tcp", addr)
		if err != nil {
			srv.Stop()
			return nil, fmt.Errorf("plugins.listen %s: %w", addr, err)
		}
		m.mu.Lock()
		m.listening = tl.Addr().String()
		m.mu.Unlock()
		m.log.Info("attached plugins can connect", "addr", tl.Addr().String())
		go func() { _ = srv.Serve(tl) }()
	}
	return srv, nil
}

type authedStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *authedStream) Context() context.Context { return s.ctx }

type hostServer struct {
	pluginv1.UnimplementedPluginHostServer
	m *Manager
}

func pluginFrom(ctx context.Context) *plugin { return ctx.Value(ctxKey{}).(*plugin) }

// granted checks a permission and that the plugin has a session open.
func (h *hostServer) granted(ctx context.Context, perm string) (*plugin, error) {
	p := pluginFrom(ctx)
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if p.sess == nil {
		return nil, status.Error(codes.FailedPrecondition, "open the Session stream first")
	}
	if perm != "" {
		if _, granted, _ := p.effective(); !slices.Contains(granted, perm) {
			return nil, status.Errorf(codes.PermissionDenied, "the plugin hasn't been granted %s", perm)
		}
	}
	return p, nil
}

func (h *hostServer) Session(stream pluginv1.PluginHost_SessionServer) error {
	m := h.m
	p := pluginFrom(stream.Context())
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	switch {
	case hello == nil:
		return status.Error(codes.InvalidArgument, "the first message must be Hello")
	case hello.PluginId != p.id:
		return status.Errorf(codes.PermissionDenied, "this token belongs to %s, not %s", p.id, hello.PluginId)
	case hello.ApiVersion != APIVersion:
		return status.Errorf(codes.FailedPrecondition, "plugin speaks API %d; this RepeaterTastic speaks %d", hello.ApiVersion, APIVersion)
	}

	m.mu.Lock()
	if p.rec.Attached && hello.ManifestYaml != "" && hello.ManifestYaml != p.rec.ManifestYAML {
		man, err := ParseManifest([]byte(hello.ManifestYaml))
		if err == nil && man.ID != p.id {
			err = fmt.Errorf("its manifest id is %s", man.ID)
		}
		if err != nil {
			m.mu.Unlock()
			return status.Errorf(codes.InvalidArgument, "manifest: %v", err)
		}
		p.rec.ManifestYAML, p.manifest = hello.ManifestYaml, man
		_ = m.saveLocked()
	}
	if st, detail := p.blocker(); st != "" && !(st == "waiting" && p.rec.Attached) {
		p.state, p.detail = st, detail
		m.mu.Unlock()
		m.notify(p.id)
		return status.Errorf(codes.FailedPrecondition, "the plugin can't run: %s", strings.TrimSpace(st+" "+detail))
	}
	tok := ""
	if !p.rec.Attached {
		tok = p.token
	}
	sess := &session{token: tok, out: make(chan *pluginv1.HostMessage, 2048), done: make(chan struct{})}
	old := p.sess
	p.sess, p.connected, p.status, p.panel = sess, time.Now(), nil, ""
	p.state, p.detail = "running", ""
	_, granted, settings := p.effective()
	m.mu.Unlock()
	if old != nil {
		old.close("replaced by a new session")
	}
	p.logs.add("info", "host", fmt.Sprintf("connected (%s %s)", p.id, hello.PluginVersion))
	m.notify(p.id)

	defer func() {
		sess.close("stream ended")
		m.mu.Lock()
		if p.sess == sess {
			p.sess = nil
			if p.state == "running" {
				p.state, p.detail = "starting", "Disconnected; waiting for the plugin to connect again"
				if p.rec.Attached {
					p.state = "waiting"
				}
			}
		}
		m.mu.Unlock()
		p.logs.add("info", "host", "disconnected")
		m.notify(p.id)
	}()

	welcome := &pluginv1.Welcome{ApiVersion: APIVersion, HostVersion: m.opt.Version, Permissions: granted,
		SettingsJson: jsonString(settings), Radios: m.radiosProto()}
	if !p.rec.Attached {
		welcome.DataDir = m.dataRoot() + string(os.PathSeparator) + p.id
	}
	if err := stream.Send(&pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Welcome{Welcome: welcome}}); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(stream.Context())
	defer cancel()
	for _, r := range m.opt.Radios {
		go m.pump(ctx, sess, p, r, granted)
	}
	recvErr := make(chan error, 1)
	go func() { recvErr <- h.receive(stream, p) }()
	for {
		select {
		case msg := <-sess.out:
			if err := stream.Send(msg); err != nil {
				return err
			}
			if msg.GetStop() != nil {
				// Give the plugin a moment to close the stream itself.
				select {
				case err := <-recvErr:
					return ignoreEOF(err)
				case <-time.After(stopGrace):
					return status.Error(codes.Unavailable, "stopped")
				}
			}
		case err := <-recvErr:
			return ignoreEOF(err)
		case <-sess.done:
			reason, _ := sess.reason.Load().(string)
			return status.Error(codes.Unavailable, reason)
		}
	}
}

func ignoreEOF(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "EOF") {
		return nil
	}
	return err
}

// receive handles what the plugin sends on the session.
func (h *hostServer) receive(stream pluginv1.PluginHost_SessionServer, p *plugin) error {
	m := h.m
	for {
		msg, err := stream.Recv()
		if err != nil {
			return err
		}
		switch v := msg.Msg.(type) {
		case *pluginv1.PluginMessage_Status:
			st := v.Status
			if len(st.Fields) > 40 {
				continue
			}
			m.mu.Lock()
			p.status = st
			m.mu.Unlock()
			m.notify(p.id)
		case *pluginv1.PluginMessage_Log:
			level := v.Log.Level
			if !slices.Contains([]string{"debug", "info", "warn", "error"}, level) {
				level = "info"
			}
			p.logs.add(level, "plugin", v.Log.Message)
		case *pluginv1.PluginMessage_Panel:
			if len(v.Panel.Json) > 1<<20 || !json.Valid([]byte(v.Panel.Json)) {
				p.logs.add("warn", "host", "ignored panel data: not JSON or larger than 1 MB")
				continue
			}
			m.mu.Lock()
			p.panel = v.Panel.Json
			m.mu.Unlock()
			m.notify(p.id)
		}
	}
}

// pump forwards one radio's bus events the plugin may see.
// tracesFor: traceroute results reach a plugin for the identity it sends them from.
func (m *Manager) tracesFor(p *plugin, r Radio, identity string) bool {
	m.mu.Lock()
	_, _, settings := p.effective()
	var schema []Setting
	if p.manifest != nil {
		schema = p.manifest.Settings
	}
	m.mu.Unlock()
	chosen := chosenIdentities(schema, settings, r.Host)
	if len(chosen) == 0 {
		relay := r.Host.Relay()
		return relay != nil && relay.NodeID() == identity
	}
	return slices.ContainsFunc(chosen, func(id *mesh.Identity) bool { return id.NodeID() == identity })
}

func (m *Manager) pump(ctx context.Context, sess *session, p *plugin, r Radio, granted []string) {
	has := func(perm string) bool { return slices.Contains(granted, perm) }
	if !has("packets.read") && !has("nodes.read") && !has("messages.read") && !has("traceroute.send") {
		return
	}
	events, unsubscribe := r.Host.Bus.Subscribe(512)
	defer unsubscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.done:
			return
		case e, ok := <-events:
			if !ok {
				return
			}
			var msg *pluginv1.HostMessage
			switch e.Type {
			case "packet":
				if rec, ok := e.Data.(mesh.PacketRecord); ok && has("packets.read") {
					msg = packetEvent(r, rec)
				}
			case "node":
				if id, ok := e.Data.(string); ok && has("nodes.read") {
					if num, err := wire.ParseNodeID(id); err == nil {
						if n, ok := r.Host.DB.Get(num); ok {
							msg = &pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Node{Node: &pluginv1.NodeEvent{Node: nodeProto(r.ID, n)}}}
						}
					}
				}
			case "message":
				if me, ok := e.Data.(mesh.MessageEvent); ok && has("messages.read") {
					msg = textEvent(r, me)
				}
			case "traceroute":
				if tr, ok := e.Data.(mesh.TracerouteResult); ok && (has("traceroute.send") || has("nodes.read")) {
					if m.tracesFor(p, r, tr.Identity) {
						msg = &pluginv1.HostMessage{Msg: &pluginv1.HostMessage_Traceroute{Traceroute: &pluginv1.TracerouteEvent{
							RadioId: r.ID, IdentityNodeId: tr.Identity, TargetNodeId: tr.Target, Route: tr.Route,
							SnrTowards: tr.SNRTowards, RouteBack: tr.RouteBack, SnrBack: tr.SNRBack}}}
					}
				}
			}
			if msg != nil {
				sess.send(msg)
			}
		}
	}
}

func (h *hostServer) ListRadios(ctx context.Context, _ *pluginv1.ListRadiosRequest) (*pluginv1.ListRadiosResponse, error) {
	if _, err := h.granted(ctx, ""); err != nil {
		return nil, err
	}
	return &pluginv1.ListRadiosResponse{Radios: h.m.radiosProto()}, nil
}

func (h *hostServer) ListNodes(ctx context.Context, req *pluginv1.ListNodesRequest) (*pluginv1.ListNodesResponse, error) {
	if _, err := h.granted(ctx, "nodes.read"); err != nil {
		return nil, err
	}
	out := &pluginv1.ListNodesResponse{}
	for _, r := range h.m.opt.Radios {
		if req.RadioId != "" && r.ID != req.RadioId {
			continue
		}
		for _, n := range r.Host.DB.Snapshot() {
			out.Nodes = append(out.Nodes, nodeProto(r.ID, n))
		}
	}
	return out, nil
}

func (h *hostServer) SendText(ctx context.Context, req *pluginv1.SendTextRequest) (*pluginv1.SendResponse, error) {
	p, err := h.granted(ctx, "messages.send")
	if err != nil {
		return nil, err
	}
	r := h.m.radio(req.RadioId)
	if r == nil {
		return nil, status.Errorf(codes.NotFound, "no radio %q", req.RadioId)
	}
	to := wire.Broadcast
	if req.To != "" {
		if to, err = wire.ParseNodeID(req.To); err != nil {
			return nil, status.Error(codes.InvalidArgument, "to must be a node id like !a1c40e07")
		}
	}
	if n := len(req.Text); n == 0 || n > 200 {
		return nil, status.Error(codes.InvalidArgument, "text must be 1-200 bytes")
	}
	if !r.Host.Transmits() {
		return nil, status.Errorf(codes.FailedPrecondition, "%s isn't transmitting (monitor or off)", r.ID)
	}
	if ok, wait := p.msgBudget.take(); !ok {
		return nil, budgetError("messages", p.msgBudget.rate(), wait)
	}
	relay := r.Host.Relay()
	pid, err := r.Host.SendText(relay, to, int(req.Channel), req.Text, req.WantAck)
	if errors.Is(err, mesh.ErrNotTransmitting) {
		return nil, status.Errorf(codes.FailedPrecondition, "%s isn't transmitting (monitor or off)", r.ID)
	}
	if err != nil && pid == 0 {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	p.logs.add("info", "host", fmt.Sprintf("sent a message from %s to %s on %s", relay.NodeID(), nodeOrChannel(req.To, req.Channel), r.ID))
	return &pluginv1.SendResponse{PacketId: pid}, nil
}

func (h *hostServer) Traceroute(ctx context.Context, req *pluginv1.TracerouteRequest) (*pluginv1.SendResponse, error) {
	p, err := h.granted(ctx, "traceroute.send")
	if err != nil {
		return nil, err
	}
	r := h.m.radio(req.RadioId)
	if r == nil {
		return nil, status.Errorf(codes.NotFound, "no radio %q", req.RadioId)
	}
	target, err := wire.ParseNodeID(req.Target)
	if err != nil || target == wire.Broadcast {
		return nil, status.Error(codes.InvalidArgument, "target must be a node id like !a1c40e07")
	}
	// A plugin sends only from the identity chosen for it in its settings on that radio, or the
	// relay persona when none is chosen there.
	h.m.mu.Lock()
	_, _, settings := p.effective()
	var schema []Setting
	if p.manifest != nil {
		schema = p.manifest.Settings
	}
	h.m.mu.Unlock()
	allowed := chosenIdentities(schema, settings, r.Host)
	from := r.Host.Relay()
	if len(allowed) > 0 {
		from = allowed[0]
	}
	if req.From != "" {
		num, err := wire.ParseNodeID(req.From)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "from must be a node id like !a1c40e07")
		}
		if !slices.ContainsFunc(append(allowed, from), func(id *mesh.Identity) bool { return id.NodeNum == num }) {
			return nil, status.Errorf(codes.PermissionDenied, "%s isn't the identity chosen in the plugin's settings for %s", req.From, r.ID)
		}
		from = r.Host.Identity(num)
	}
	if !r.Host.Transmits() {
		return nil, status.Errorf(codes.FailedPrecondition, "%s isn't transmitting (monitor or off)", r.ID)
	}
	if ok, wait := p.trBudget.take(); !ok {
		return nil, budgetError("traceroutes", p.trBudget.rate(), wait)
	}
	if err := r.Host.Traceroute(from, target); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}
	p.logs.add("info", "host", fmt.Sprintf("sent a traceroute from %s to %s on %s", from.NodeID(), req.Target, r.ID))
	return &pluginv1.SendResponse{}, nil
}

// chosenIdentities are the identities on this radio picked in the plugin's "identities" settings.
func chosenIdentities(schema []Setting, settings map[string]any, host *mesh.Host) []*mesh.Identity {
	var out []*mesh.Identity
	for _, s := range schema {
		if s.Type != "identities" {
			continue
		}
		var ids []string
		switch v := settings[s.Key].(type) {
		case []string:
			ids = v
		case []any:
			for _, x := range v {
				if str, ok := x.(string); ok {
					ids = append(ids, str)
				}
			}
		}
		for _, str := range ids {
			num, err := wire.ParseNodeID(str)
			if err != nil {
				continue
			}
			if id := host.Identity(num); id != nil && !slices.Contains(out, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

func budgetError(what string, perHour int, wait time.Duration) error {
	if perHour <= 0 {
		return status.Errorf(codes.PermissionDenied, "plugins may not send %s (plugins.%s_per_hour is 0)", what, what)
	}
	return status.Errorf(codes.ResourceExhausted, "send budget used up (%d %s an hour); try again in %s", perHour, what, wait.Round(time.Second))
}

func nodeOrChannel(to string, ch uint32) string {
	if to != "" {
		return to
	}
	return fmt.Sprintf("channel %d", ch)
}
