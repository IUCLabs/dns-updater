package records

import (
	"strings"
	"testing"
)

func TestFQDN(t *testing.T) {
	cases := []struct {
		name, zone, want string
	}{
		{"host2070", "int.example.net", "host2070.int.example.net."},
		{"@", "int.example.net", "int.example.net."},
		{"", "int.example.net", "int.example.net."},
		// The regression: an already-qualified name must NOT get the zone
		// appended again (that produced the ".." empty label in the shell bug).
		{"site1-pve.int.example.net.", "int.example.net", "site1-pve.int.example.net."},
		{"cobbler.int.example.net.", "int.example.net", "cobbler.int.example.net."},
		{"18", "100.51.198.in-addr.arpa", "18.100.51.198.in-addr.arpa."},
	}
	for _, c := range cases {
		if got := FQDN(c.name, c.zone); got != c.want {
			t.Errorf("FQDN(%q,%q)=%q want %q", c.name, c.zone, got, c.want)
		}
		if strings.Contains(FQDN(c.name, c.zone), "..") {
			t.Errorf("FQDN(%q,%q) produced an empty label", c.name, c.zone)
		}
	}
}

func TestRRValidatesName(t *testing.T) {
	// A well-formed FQDN record renders cleanly.
	good := Record{Zone: "int.example.net", Name: "site1-pve.int.example.net.", Type: "CNAME", TTL: 300, Value: "site1-prod-halb.int.example.net."}
	if _, err := good.RR(); err != nil {
		t.Fatalf("good record: unexpected error %v", err)
	}
	// An empty label must be rejected, not silently emitted.
	bad := Record{Zone: "int.example.net", Name: "broken..name.", Type: "A", TTL: 300, Value: "198.51.100.18"}
	if _, err := bad.RR(); err == nil {
		t.Fatalf("bad record: expected error for empty label, got nil")
	}
}

func TestParseAndSet(t *testing.T) {
	in := `# comment
int.example.net|host2070|A|300|198.51.100.18

100.51.198.in-addr.arpa|18|PTR|300|host2070.int.example.net.
int.example.net|site1-pve.int.example.net.|CNAME|300|site1-prod-halb.int.example.net.`
	set, err := parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if set.Len() != 3 {
		t.Fatalf("len=%d want 3", set.Len())
	}
	zones := set.Zones()
	if len(zones) != 2 {
		t.Fatalf("zones=%v want 2", zones)
	}
	// zones are fully-qualified and sorted
	if zones[0] != "100.51.198.in-addr.arpa." || zones[1] != "int.example.net." {
		t.Errorf("zones=%v", zones)
	}
}

func TestParseReportsBadLine(t *testing.T) {
	in := "int.example.net|host|A|notanumber|1.2.3.4\nint.example.net|ok|A|300|1.2.3.4"
	set, err := parse(strings.NewReader(in))
	if err == nil {
		t.Fatal("expected error for bad ttl")
	}
	// the good record is still collected
	if set.Len() != 1 {
		t.Errorf("len=%d want 1 (good record kept)", set.Len())
	}
}
