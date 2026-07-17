package updater

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/IUCLabs/dns-updater/internal/records"
	"github.com/IUCLabs/dns-updater/internal/tsig"
)

// fakeServer is an in-process TSIG-verifying DNS server that records the update
// messages it receives and replies with a per-zone rcode.
type fakeServer struct {
	addr     string
	srv      *dns.Server
	key      *tsig.Key
	mu       sync.Mutex
	received []*dns.Msg
	rcodes   map[string]int // zone (fqdn) -> rcode
}

func newFakeServer(t *testing.T, key *tsig.Key) *fakeServer {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	fs := &fakeServer{addr: l.Addr().String(), key: key, rcodes: map[string]int{}}
	fs.srv = &dns.Server{
		Listener:   l,
		Net:        "tcp",
		TsigSecret: key.SecretMap(),
		// The default accept func rejects the UPDATE opcode with NOTIMP; a real
		// BIND update server accepts it, so opt in here.
		MsgAcceptFunc: func(dns.Header) dns.MsgAcceptAction { return dns.MsgAccept },
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
			fs.mu.Lock()
			fs.received = append(fs.received, r)
			rc := dns.RcodeSuccess
			if len(r.Question) > 0 {
				if v, ok := fs.rcodes[r.Question[0].Name]; ok {
					rc = v
				}
			}
			fs.mu.Unlock()
			m := new(dns.Msg)
			m.SetReply(r)
			m.Rcode = rc
			if r.IsTsig() != nil {
				m.SetTsig(key.Name, key.Algorithm, 300, time.Now().Unix())
			}
			_ = w.WriteMsg(m)
		}),
	}
	go func() { _ = fs.srv.ActivateAndServe() }()
	// give the server a moment to start accepting
	time.Sleep(50 * time.Millisecond)
	t.Cleanup(func() { _ = fs.srv.Shutdown() })
	return fs
}

func testKey() *tsig.Key {
	return &tsig.Key{Name: "test-key.", Algorithm: dns.HmacSHA256, Secret: "dGVzdHNlY3JldA=="}
}

func mustSet(t *testing.T, recs ...records.Record) *records.Set {
	t.Helper()
	s := records.NewSet()
	for _, r := range recs {
		if err := s.Add(r); err != nil {
			t.Fatalf("add %v: %v", r, err)
		}
	}
	return s
}

func TestReconcileAddsAndSigns(t *testing.T) {
	key := testKey()
	fs := newFakeServer(t, key)
	app := New(fs.addr, key, 3*time.Second)

	desired := mustSet(t,
		records.Record{Zone: "int.example.net", Name: "host1", Type: "A", TTL: 300, Value: "198.51.100.18"},
		records.Record{Zone: "int.example.net", Name: "site1-pve.int.example.net.", Type: "CNAME", TTL: 300, Value: "host1.int.example.net."},
		records.Record{Zone: "100.51.198.in-addr.arpa", Name: "18", Type: "PTR", TTL: 300, Value: "host1.int.example.net."},
	)
	res := app.Reconcile(desired, records.NewSet())
	if !res.OK() {
		t.Fatalf("reconcile not OK: %s", res.String())
	}
	// two zones, both applied
	if len(res.Zones) != 2 {
		t.Fatalf("zones=%d want 2 (%s)", len(res.Zones), res.String())
	}
	fs.mu.Lock()
	got := len(fs.received)
	fs.mu.Unlock()
	if got != 2 {
		t.Fatalf("server received %d messages want 2", got)
	}
}

func TestReconcileDeletesRemoved(t *testing.T) {
	key := testKey()
	fs := newFakeServer(t, key)
	app := New(fs.addr, key, 3*time.Second)

	applied := mustSet(t,
		records.Record{Zone: "int.example.net", Name: "host1", Type: "A", TTL: 300, Value: "198.51.100.18"},
		records.Record{Zone: "int.example.net", Name: "gone", Type: "A", TTL: 300, Value: "198.51.100.99"},
	)
	desired := mustSet(t,
		records.Record{Zone: "int.example.net", Name: "host1", Type: "A", TTL: 300, Value: "198.51.100.18"},
	)
	res := app.Reconcile(desired, applied)
	if !res.OK() {
		t.Fatalf("not OK: %s", res.String())
	}
	if len(res.Zones) != 1 || res.Zones[0].Deletes != 1 {
		t.Fatalf("want 1 delete, got %s", res.String())
	}
}

func TestReconcileZoneRcodeFailure(t *testing.T) {
	key := testKey()
	fs := newFakeServer(t, key)
	fs.rcodes["storage.example.net."] = dns.RcodeNotZone // simulate NOTZONE
	app := New(fs.addr, key, 3*time.Second)

	desired := mustSet(t,
		records.Record{Zone: "int.example.net", Name: "host1", Type: "A", TTL: 300, Value: "198.51.100.18"},
		records.Record{Zone: "storage.example.net", Name: "dashboard.storage.example.net.", Type: "CNAME", TTL: 300, Value: "host1.int.example.net."},
	)
	res := app.Reconcile(desired, records.NewSet())
	if res.OK() {
		t.Fatal("expected failure due to NOTZONE")
	}
	// The good zone still applied; only storage failed — the "one bad zone must not
	// abort the others" property that the shell version lacked.
	var mainOK, storageFailed bool
	for _, z := range res.Zones {
		if z.Zone == "int.example.net." && z.OK() {
			mainOK = true
		}
		if z.Zone == "storage.example.net." && !z.OK() {
			storageFailed = true
		}
	}
	if !mainOK || !storageFailed {
		t.Fatalf("mainOK=%v storageFailed=%v (%s)", mainOK, storageFailed, res.String())
	}
	// Applied state keeps the good zone, drops the failed one for retry.
	appliedNow := res.Applied(desired, records.NewSet())
	if appliedNow.Len() != 1 {
		t.Errorf("applied=%d want 1 (only main)", appliedNow.Len())
	}
}

func TestReconcileNoChanges(t *testing.T) {
	key := testKey()
	fs := newFakeServer(t, key)
	app := New(fs.addr, key, 3*time.Second)
	same := mustSet(t, records.Record{Zone: "int.example.net", Name: "host1", Type: "A", TTL: 300, Value: "198.51.100.18"})
	res := app.Reconcile(same, same)
	if res.String() != "no changes" {
		t.Fatalf("want no changes, got %s", res.String())
	}
	fs.mu.Lock()
	got := len(fs.received)
	fs.mu.Unlock()
	if got != 0 {
		t.Errorf("server got %d messages, want 0 (nothing to do)", got)
	}
}
