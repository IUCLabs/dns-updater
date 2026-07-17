// Package updater pushes the desired records to a BIND server via RFC2136
// dynamic update (TSIG-signed), sending only the delta since the last applied
// state. Each zone is updated in its own message so one bad zone cannot abort
// the others, and every zone's result carries the server RCODE.
package updater

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/miekg/dns"

	"github.com/IUCLabs/dns-updater/internal/records"
	"github.com/IUCLabs/dns-updater/internal/tsig"
)

// Applier sends RFC2136 updates to a single server.
type Applier struct {
	server  string
	key     *tsig.Key
	client  *dns.Client
	fudge   uint16
	timeout time.Duration
}

// New builds an Applier. server is host:port; key signs the updates.
func New(server string, key *tsig.Key, timeout time.Duration) *Applier {
	c := &dns.Client{
		Net:          "tcp", // updates use TCP; also lets responses exceed 512 bytes
		TsigSecret:   key.SecretMap(),
		DialTimeout:  timeout,
		ReadTimeout:  timeout,
		WriteTimeout: timeout,
	}
	return &Applier{server: server, key: key, client: c, fudge: 300, timeout: timeout}
}

// ZoneResult is the outcome of updating one zone.
type ZoneResult struct {
	Zone    string
	Adds    int
	Deletes int
	Rcode   int
	Err     error
}

// OK reports whether the zone update succeeded.
func (z ZoneResult) OK() bool { return z.Err == nil && z.Rcode == dns.RcodeSuccess }

// Result aggregates per-zone outcomes for one reconcile.
type Result struct {
	Zones []ZoneResult
}

// OK reports whether every zone update succeeded.
func (r Result) OK() bool {
	for _, z := range r.Zones {
		if !z.OK() {
			return false
		}
	}
	return true
}

// Applied returns the keys of records that are now live on the server: for a
// failed zone, its records keep their previous applied state (from prev) so we
// retry them next time; for a succeeded zone, desired wins.
func (r Result) Applied(desired, prev *records.Set) *records.Set {
	failed := map[string]bool{}
	for _, z := range r.Zones {
		if !z.OK() {
			failed[z.Zone] = true
		}
	}
	out := records.NewSet()
	// desired records in succeeded zones are now applied
	for _, rec := range desired.Records() {
		if !failed[dns.Fqdn(rec.Zone)] {
			_ = out.Add(rec)
		}
	}
	// records from failed zones retain their previous applied state
	for _, rec := range prev.Records() {
		if failed[dns.Fqdn(rec.Zone)] {
			_ = out.Add(rec)
		}
	}
	return out
}

// Reconcile computes the delta between desired and applied and pushes one update
// per zone. applied is the last-known server state (empty on first run).
func (a *Applier) Reconcile(desired, applied *records.Set) Result {
	// Union of zones touched by either desired or applied records.
	zoneSet := map[string]struct{}{}
	for _, z := range desired.Zones() {
		zoneSet[z] = struct{}{}
	}
	for _, z := range applied.Zones() {
		zoneSet[z] = struct{}{}
	}
	zones := make([]string, 0, len(zoneSet))
	for z := range zoneSet {
		zones = append(zones, z)
	}
	sort.Strings(zones)

	dz := keysByZone(desired)
	az := keysByZone(applied)

	var res Result
	for _, zone := range zones {
		// Skip zones whose desired record set already matches applied — this is
		// what keeps steady-state resyncs and interface flaps from sending (and
		// logging) anything.
		if equalKeys(dz[zone], az[zone]) {
			continue
		}
		res.Zones = append(res.Zones, a.reconcileZone(zone, desired, applied))
	}
	return res
}

// keysByZone maps each zone (fqdn) to the set of record keys it contains.
func keysByZone(s *records.Set) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, r := range s.Records() {
		z := dns.Fqdn(r.Zone)
		if out[z] == nil {
			out[z] = map[string]bool{}
		}
		out[z][r.Key()] = true
	}
	return out
}

func equalKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

func (a *Applier) reconcileZone(zone string, desired, applied *records.Set) ZoneResult {
	zr := ZoneResult{Zone: zone}

	msg := new(dns.Msg)
	msg.SetUpdate(zone)

	// Additions/updates: replace the RRset for every desired record in the zone.
	// Grouping by owner+type first lets us RemoveRRset once then Insert all
	// values, so multi-value RRsets (round-robin A) are not clobbered.
	type ot struct{ owner, typ string }
	byRRset := map[ot][]dns.RR{}
	for _, rec := range desired.Records() {
		if dns.Fqdn(rec.Zone) != zone {
			continue
		}
		rr, err := rec.RR()
		if err != nil {
			zr.Err = err // should not happen: Set.Add already validated
			return zr
		}
		k := ot{rr.Header().Name, dns.TypeToString[rr.Header().Rrtype]}
		byRRset[k] = append(byRRset[k], rr)
	}
	rrsetKeys := make([]ot, 0, len(byRRset))
	for k := range byRRset {
		rrsetKeys = append(rrsetKeys, k)
	}
	sort.Slice(rrsetKeys, func(i, j int) bool {
		if rrsetKeys[i].owner != rrsetKeys[j].owner {
			return rrsetKeys[i].owner < rrsetKeys[j].owner
		}
		return rrsetKeys[i].typ < rrsetKeys[j].typ
	})
	for _, k := range rrsetKeys {
		rrs := byRRset[k]
		msg.RemoveRRset(rrs)
		msg.Insert(rrs)
		zr.Adds += len(rrs)
	}

	// Deletions: records present last run but gone now.
	for _, rec := range applied.Records() {
		if dns.Fqdn(rec.Zone) != zone || desired.Has(rec.Key()) {
			continue
		}
		rr, err := rec.RR()
		if err != nil {
			continue
		}
		msg.Remove([]dns.RR{rr})
		zr.Deletes++
	}

	if zr.Adds == 0 && zr.Deletes == 0 {
		return zr // nothing to do for this zone
	}

	msg.SetTsig(a.key.Name, a.key.Algorithm, a.fudge, time.Now().Unix())

	resp, _, err := a.client.Exchange(msg, a.server)
	if err != nil {
		zr.Err = fmt.Errorf("exchange with %s: %w", a.server, err)
		return zr
	}
	zr.Rcode = resp.Rcode
	if resp.Rcode != dns.RcodeSuccess {
		zr.Err = fmt.Errorf("zone %s: server rcode %s", zone, dns.RcodeToString[resp.Rcode])
	}
	return zr
}

// String renders a result for logging.
func (r Result) String() string {
	var b strings.Builder
	for i, z := range r.Zones {
		if i > 0 {
			b.WriteString(" ")
		}
		status := dns.RcodeToString[z.Rcode]
		if z.Err != nil && z.Rcode == dns.RcodeSuccess {
			status = "ERR"
		}
		fmt.Fprintf(&b, "%s(+%d-%d %s)", strings.TrimSuffix(z.Zone, "."), z.Adds, z.Deletes, status)
	}
	if b.Len() == 0 {
		return "no changes"
	}
	return b.String()
}
