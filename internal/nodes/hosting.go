package nodes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ScotMesh/RepeaterTastic/internal/mesh"
	"github.com/ScotMesh/RepeaterTastic/internal/wire"
)

// SlotsPerRadio is the size of each radio's block of client API ports: the relay persona takes
// the first, identities the rest.
const SlotsPerRadio = 100

// HostingOptions say which of a radio's identities run on meshtasticd, and how.
type HostingOptions struct {
	Launcher Launcher
	Air      Air
	Radio    string // radio ID: names the instances
	Dir      string // instance state directories live here
	PortBase int    // the radio's first client API port (the persona's)
	// Persona runs the relay persona on meshtasticd (unless the air brings its own relay).
	Persona bool
	// Identities runs the other identities on meshtasticd too (not those routed across radios).
	Identities bool
	// RelayOwner names the persona (the configured relay names).
	RelayOwner func() (long, short string)
	Logf       func(string, ...any)
}

// Hosting runs a radio's identities as hosted meshtasticd nodes on its air: a mesh.Hoster.
type Hosting struct {
	ctx  context.Context // instances live as long as this
	opts HostingOptions

	mu       sync.Mutex
	nodes    map[*Node]*hostedEntry
	starting map[int]bool // port slots held by starts in progress
}

type hostedEntry struct {
	hn      *Hosted
	host    *mesh.Host
	slot    int
	role    string
	running chan struct{} // closed when the node's event loop has ended
}

var _ mesh.Hoster = (*Hosting)(nil)

// NewHosting runs hosted nodes until ctx ends.
func NewHosting(ctx context.Context, o HostingOptions) *Hosting {
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	return &Hosting{ctx: ctx, opts: o, nodes: map[*Node]*hostedEntry{}, starting: map[int]bool{}}
}

// Takes reports whether the record runs on meshtasticd.
func (x *Hosting) Takes(rec mesh.IdentityRecord) bool {
	if rec.IsRelay {
		return x.opts.Persona && x.opts.Air.Relay() == nil
	}
	return x.opts.Identities && rec.MultiRadio == nil
}

// HostIdentity starts a meshtasticd for rec, seeded with its key, and adds its identity to h.
func (x *Hosting) HostIdentity(ctx context.Context, h *mesh.Host, rec mesh.IdentityRecord) (*mesh.Identity, error) {
	if !x.Takes(rec) {
		return nil, mesh.ErrRunHere
	}
	num := rec.NodeNum()
	if num == 0 {
		return nil, fmt.Errorf("bad key for %q", rec.LongName)
	}
	x.mu.Lock()
	slot, role := 0, "persona"
	if !rec.IsRelay {
		role = "identity"
		if slot = x.freeSlot(); slot == 0 {
			x.mu.Unlock()
			return nil, fmt.Errorf("no free port: a radio hosts at most %d identities", SlotsPerRadio-1)
		}
	}
	x.starting[slot] = true
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		delete(x.starting, slot)
		x.mu.Unlock()
	}()

	nodeID := strings.TrimPrefix(wire.NodeID(num), "!")
	in := Instance{Name: "persona-" + x.opts.Radio, Dir: filepath.Join(x.opts.Dir, "persona"),
		Port: x.opts.PortBase, HWID: HWIDFor(x.opts.Radio + "/persona")}
	if !rec.IsRelay {
		in = Instance{Name: x.opts.Radio + "-" + nodeID, Dir: filepath.Join(x.opts.Dir, nodeID),
			Port: x.opts.PortBase + slot, HWID: HWIDFor(x.opts.Radio + "/" + nodeID)}
	}
	hn, err := StartHosted(x.ctx, x.opts.Launcher, in, x.opts.Logf)
	if err != nil {
		return nil, err
	}
	if rec.IsRelay && x.opts.RelayOwner != nil {
		hn.SetOwner(x.opts.RelayOwner())
	}
	hn.SetSeed(rec)
	id, err := hn.Identity(ctx, 0)
	if err == nil {
		err = h.AddIdentity(id)
	}
	if err != nil {
		hn.Stop()
		return nil, err
	}
	h.AddConfigApplier(hn)
	hn.Bind(h, id)
	done := make(chan struct{})
	go func() {
		defer close(done)
		hn.Run(hn.Context())
	}()
	x.opts.Air.Join(hn.Context(), hn.Node)
	x.mu.Lock()
	x.nodes[hn.Node] = &hostedEntry{hn: hn, host: h, slot: slot, role: role, running: done}
	x.mu.Unlock()
	x.opts.Logf("meshtasticd: %s %s runs on %s, port %d", role, id.NodeID(), x.opts.Launcher.Describe(), in.Port)
	return id, nil
}

// freeSlot is the lowest identity port slot not in use, or 0. Called with mu held.
func (x *Hosting) freeSlot() int {
	used := map[int]bool{}
	for s := range x.starting {
		used[s] = true
	}
	for _, e := range x.nodes {
		used[e.slot] = true
	}
	for s := 1; s < SlotsPerRadio; s++ {
		if !used[s] {
			return s
		}
	}
	return 0
}

// Unhost stops the meshtasticd an identity stands for and deletes its state (the identity's key
// and settings are the host's to keep).
func (x *Hosting) Unhost(id *mesh.Identity) {
	n, _ := id.Remote().(*Node)
	x.mu.Lock()
	e := x.nodes[n]
	delete(x.nodes, n)
	x.mu.Unlock()
	if e == nil {
		return
	}
	e.host.RemoveConfigApplier(e.hn)
	x.opts.Air.Leave(e.hn.Node)
	e.hn.Stop()
	<-e.running
	if e.role != "persona" {
		if err := os.RemoveAll(e.hn.Instance().Dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			x.opts.Logf("meshtasticd: removing %s: %v", e.hn.Instance().Dir, err)
		}
	}
	x.opts.Logf("meshtasticd: %s %s stopped", e.role, id.NodeID())
}

// HostedNode is one running instance, for the GUI.
type HostedNode struct {
	Role string // persona or identity
	HostedStatus
}

// Nodes lists the running instances, persona first, then by port.
func (x *Hosting) Nodes() []HostedNode {
	x.mu.Lock()
	var out []HostedNode
	for _, e := range x.nodes {
		out = append(out, HostedNode{Role: e.role, HostedStatus: e.hn.Status()})
	}
	x.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

// Launcher runs the instances.
func (x *Hosting) Launcher() Launcher { return x.opts.Launcher }
