package records

import (
	"net"
	"testing"
)

func cidrs(t *testing.T, ss ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, s := range ss {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			t.Fatalf("bad cidr %q: %v", s, err)
		}
		out = append(out, n)
	}
	return out
}

func TestFilterDenyRanges(t *testing.T) {
	f := &Filter{DenyRanges: cidrs(t, "203.0.113.128/25", "203.0.113.0/25", "192.0.2.0/25", "192.0.2.128/25")}
	cases := []struct {
		rec  Record
		keep bool
	}{
		// real host A records — kept
		{Record{Zone: "int.example.net", Name: "host2071", Type: "A", Value: "198.51.100.41"}, true},
		{Record{Zone: "int.example.net", Name: "node0001", Type: "A", Value: "198.51.100.191"}, true},
		// k8s/LB junk A records — dropped by value
		{Record{Zone: "int.example.net", Name: "node0001-flannel.1", Type: "A", Value: "192.0.2.0"}, false},
		{Record{Zone: "int.example.net", Name: "node0001-kube-lb0", Type: "A", Value: "203.0.113.2"}, false},
		// PTRs in the excluded reverse zones — dropped by owner-derived address
		{Record{Zone: "113.0.203.in-addr.arpa", Name: "2", Type: "PTR", Value: "node0001-kube-lb0.int.example.net."}, false},
		{Record{Zone: "2.0.192.in-addr.arpa", Name: "0", Type: "PTR", Value: "node0003-flannel.1.int.example.net."}, false},
		// a legit reverse PTR — kept
		{Record{Zone: "100.51.198.in-addr.arpa", Name: "41", Type: "PTR", Value: "host2071.int.example.net."}, true},
		// CNAMEs carry no address, so range rules never touch them
		{Record{Zone: "int.example.net", Name: "git.int.example.net.", Type: "CNAME", Value: "site1-prod-halb-vrrp"}, true},
	}
	for _, c := range cases {
		if got := f.Allowed(c.rec); got != c.keep {
			ip, _ := recordIP(c.rec)
			t.Errorf("Allowed(%s %s %s)=%v want %v (ip=%v)", c.rec.Zone, c.rec.Name, c.rec.Value, got, c.keep, ip)
		}
	}
}

func TestFilterAllowRangesAndDomains(t *testing.T) {
	// allow-only ranges: only 198.51.100.0/24 addresses publish
	f := &Filter{AllowRanges: cidrs(t, "198.51.100.0/24")}
	if f.Allowed(Record{Zone: "int.example.net", Name: "x", Type: "A", Value: "192.0.2.5"}) {
		t.Error("192.0.2.5 should be excluded by allow-ranges")
	}
	if !f.Allowed(Record{Zone: "int.example.net", Name: "x", Type: "A", Value: "198.51.100.5"}) {
		t.Error("198.51.100.5 should be allowed")
	}

	// deny-domains suffix match
	fd := &Filter{DenyDomains: []string{"k8s.example.net"}}
	if fd.Allowed(Record{Zone: "k8s.example.net", Name: "foo", Type: "A", Value: "1.2.3.4"}) {
		t.Error("foo.k8s.example.net should be denied by domain")
	}
	if !fd.Allowed(Record{Zone: "int.example.net", Name: "foo", Type: "A", Value: "1.2.3.4"}) {
		t.Error("foo.int.example.net should be allowed")
	}
}

func TestFilterApplyCounts(t *testing.T) {
	f := &Filter{DenyRanges: cidrs(t, "192.0.2.0/25")}
	s := NewSet()
	for _, r := range []Record{
		{Zone: "int.example.net", Name: "a", Type: "A", TTL: 300, Value: "198.51.100.1"},
		{Zone: "int.example.net", Name: "b", Type: "A", TTL: 300, Value: "192.0.2.11"},
		{Zone: "int.example.net", Name: "c", Type: "A", TTL: 300, Value: "192.0.2.22"},
	} {
		if err := s.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	out, dropped := f.Apply(s)
	if dropped != 2 || out.Len() != 1 {
		t.Fatalf("dropped=%d kept=%d, want dropped=2 kept=1", dropped, out.Len())
	}
}

func TestEmptyFilterIsPassthrough(t *testing.T) {
	var f *Filter
	if !f.Empty() {
		t.Error("nil filter should be empty")
	}
	if !f.Allowed(Record{Zone: "z", Name: "n", Type: "A", Value: "192.0.2.1"}) {
		t.Error("nil filter should allow everything")
	}
}
