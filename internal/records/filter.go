package records

import (
	"net"
	"strings"

	"github.com/miekg/dns"
)

// Filter decides which records to publish. IP-range rules apply to records that
// carry an address (A/AAAA by value, PTR by the address encoded in the owner
// name); domain rules apply to every record by owner name. Deny always wins; a
// non-empty allow list means "only these".
type Filter struct {
	AllowRanges  []*net.IPNet
	DenyRanges   []*net.IPNet
	AllowDomains []string // fqdn suffixes
	DenyDomains  []string // fqdn suffixes
}

// Empty reports whether the filter has no rules (so it can be skipped).
func (f *Filter) Empty() bool {
	return f == nil || (len(f.AllowRanges) == 0 && len(f.DenyRanges) == 0 &&
		len(f.AllowDomains) == 0 && len(f.DenyDomains) == 0)
}

// Allowed reports whether a record passes the filter.
func (f *Filter) Allowed(r Record) bool {
	if f.Empty() {
		return true
	}
	owner := r.Owner()
	if domainMatch(owner, f.DenyDomains) {
		return false
	}
	if len(f.AllowDomains) > 0 && !domainMatch(owner, f.AllowDomains) {
		return false
	}
	if ip, ok := recordIP(r); ok {
		if inAny(ip, f.DenyRanges) {
			return false
		}
		if len(f.AllowRanges) > 0 && !inAny(ip, f.AllowRanges) {
			return false
		}
	}
	return true
}

// Apply returns a Set containing only the allowed records, and the number
// dropped.
func (f *Filter) Apply(s *Set) (*Set, int) {
	if f.Empty() {
		return s, 0
	}
	out := NewSet()
	dropped := 0
	for _, r := range s.Records() {
		if f.Allowed(r) {
			_ = out.Add(r)
		} else {
			dropped++
		}
	}
	return out, dropped
}

// recordIP returns the address a record concerns: the value for A/AAAA, or the
// address encoded in a PTR owner. ok is false for records with no address
// (CNAME/TXT/SRV/...), which range rules do not touch.
func recordIP(r Record) (net.IP, bool) {
	switch strings.ToUpper(r.Type) {
	case "A", "AAAA":
		ip := net.ParseIP(strings.TrimSpace(r.Value))
		return ip, ip != nil
	case "PTR":
		ip := ptrToIP(r.Owner())
		return ip, ip != nil
	}
	return nil, false
}

// ptrToIP converts a reverse-DNS owner (…in-addr.arpa / …ip6.arpa) to an IP.
func ptrToIP(owner string) net.IP {
	name := strings.TrimSuffix(strings.ToLower(owner), ".")
	if s := strings.TrimSuffix(name, ".in-addr.arpa"); s != name {
		labels := strings.Split(s, ".")
		if len(labels) != 4 {
			return nil
		}
		reverse(labels)
		return net.ParseIP(strings.Join(labels, "."))
	}
	if s := strings.TrimSuffix(name, ".ip6.arpa"); s != name {
		labels := strings.Split(s, ".")
		if len(labels) != 32 {
			return nil
		}
		reverse(labels)
		var b strings.Builder
		for i, l := range labels {
			if i > 0 && i%4 == 0 {
				b.WriteByte(':')
			}
			b.WriteString(l)
		}
		return net.ParseIP(b.String())
	}
	return nil
}

func reverse(s []string) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

func inAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// domainMatch reports whether name equals or is a subdomain of any suffix.
func domainMatch(name string, suffixes []string) bool {
	n := strings.TrimSuffix(strings.ToLower(dns.Fqdn(name)), ".")
	for _, suf := range suffixes {
		s := strings.TrimSuffix(strings.ToLower(dns.Fqdn(suf)), ".")
		if s == "" {
			continue
		}
		if n == s || strings.HasSuffix(n, "."+s) {
			return true
		}
	}
	return false
}
