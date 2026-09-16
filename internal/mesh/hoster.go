package mesh

import (
	"context"
	"errors"
)

// Hoster runs identities as real Meshtastic nodes (meshtasticd) instead of in this host.
type Hoster interface {
	// Takes reports whether the hoster runs this record (else the host runs it).
	Takes(rec IdentityRecord) bool
	// HostIdentity starts a node for the record, seeded with its key, adds the node's identity to
	// the host and returns it. ErrRunHere means the hoster leaves this record to the host.
	HostIdentity(ctx context.Context, h *Host, rec IdentityRecord) (*Identity, error)
	// Unhost stops the node an identity stands for and forgets its state.
	Unhost(id *Identity)
}

// ErrRunHere is returned by a Hoster for an identity the host runs itself.
var ErrRunHere = errors.New("identity runs in RepeaterTastic")

// SetHoster makes hs run this host's identities from now on (nil = run them all here).
func (h *Host) SetHoster(hs Hoster) {
	h.mu.Lock()
	h.hoster = hs
	h.mu.Unlock()
}

// Hoster is the host's hoster, or nil.
func (h *Host) Hoster() Hoster {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hoster
}

// AddRecord adds a saved identity: on a hosted node when the hoster takes it, else here. A node
// that can't be started leaves the identity here (logged), so it stays on air.
func (h *Host) AddRecord(ctx context.Context, rec IdentityRecord) (*Identity, error) {
	if hs := h.Hoster(); hs != nil {
		id, err := hs.HostIdentity(ctx, h, rec)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, ErrRunHere) {
			h.log.Error("identity stays in RepeaterTastic: its meshtasticd didn't start", "node", rec.LongName, "err", err)
		}
	}
	id, err := IdentityFromRecord(rec)
	if err != nil {
		return nil, err
	}
	if err := h.AddIdentity(id); err != nil {
		return nil, err
	}
	return id, nil
}

// DropIdentity removes an identity (not the relay persona) and stops the node it stands for.
func (h *Host) DropIdentity(num uint32) error {
	id := h.Identity(num)
	if err := h.RemoveIdentity(num); err != nil {
		return err
	}
	if hs := h.Hoster(); hs != nil && id != nil && id.Remote() != nil {
		hs.Unhost(id)
	}
	return nil
}
