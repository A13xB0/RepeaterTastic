package mesh

import (
	"sort"
	"time"
)

// Site is the radios of one mast, so each knows the others' identities and sightings.
type Site struct {
	hosts []*Host
}

// JoinSite joins a mast's hosts.
func JoinSite(hosts ...*Host) *Site {
	s := &Site{hosts: hosts}
	for _, h := range hosts {
		h.site.Store(s)
	}
	return s
}

// SiteIdentity reports whether num is an identity on any radio of this site (e.g. to ignore our
// own packets coming back from MQTT).
func (h *Host) SiteIdentity(num uint32) bool {
	if h.Identity(num) != nil {
		return true
	}
	if s := h.site.Load(); s != nil {
		for _, o := range s.hosts {
			if o != h && o.Identity(num) != nil {
				return true
			}
		}
	}
	return false
}

// Sighting is what one radio knows about a node.
type Sighting struct {
	Radio     string
	LastHeard time.Time
	SNR       float32
	RSSI      int32
	HopsAway  int
	ViaMQTT   bool
}

// Sightings lists what every radio of the site knows about a node, freshest first.
func (h *Host) Sightings(num uint32) []Sighting {
	hosts := []*Host{h}
	if s := h.site.Load(); s != nil {
		hosts = s.hosts
	}
	var out []Sighting
	for _, o := range hosts {
		if e, ok := o.DB.Get(num); ok && !e.LastHeard.IsZero() {
			out = append(out, Sighting{Radio: o.RadioID(), LastHeard: e.LastHeard, SNR: e.SNR, RSSI: e.RSSI, HopsAway: e.HopsAway, ViaMQTT: e.ViaMQTT})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastHeard.After(out[j].LastHeard) })
	return out
}
