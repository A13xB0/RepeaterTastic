package mesh

import (
	"context"
	"errors"
)

// Hoster runs identities as real Meshtastic nodes (meshtasticd).
type Hoster interface {
	// HostIdentity starts a node for the record, seeded with its key, adds the node's identity to
	// the host and returns it.
	HostIdentity(ctx context.Context, h *Host, rec IdentityRecord) (*Identity, error)
	// Unhost stops the node an identity stands for and forgets its state.
	Unhost(id *Identity)
}

// ErrNoHoster is returned when a host has nothing to run its identities on.
var ErrNoHoster = errors.New("no meshtasticd to run identities on")

// SetHoster makes hs run this host's identities.
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

// AddRecord runs a saved identity on a hosted node.
func (h *Host) AddRecord(ctx context.Context, rec IdentityRecord) (*Identity, error) {
	hs := h.Hoster()
	if hs == nil {
		return nil, ErrNoHoster
	}
	return hs.HostIdentity(ctx, h, rec)
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
