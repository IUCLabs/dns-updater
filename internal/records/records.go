// Package records parses the desired-records file and turns each line into a
// DNS resource record. The file format matches what puppet's
// profiles::dns::record emits, one record per line:
//
//	zone|name|type|ttl|value
//
// name is relative to zone, "@"/empty for the apex, or already fully qualified
// (trailing dot). Blank lines and lines beginning with '#' are ignored.
package records

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/miekg/dns"
)

// Record is one desired DNS record.
type Record struct {
	Zone  string
	Name  string // as written in the file (relative, "@", or FQDN)
	Type  string
	TTL   uint32
	Value string
}

// FQDN returns the fully-qualified owner name for a record. A name that is
// already fully qualified (trailing dot) is used verbatim; "@"/empty means the
// zone apex; anything else is treated as relative to the zone. This is the Go
// equivalent of the fixed shell fqdn() and avoids the empty-label ("..") bug.
func FQDN(name, zone string) string {
	switch {
	case name == "" || name == "@":
		return dns.Fqdn(zone)
	case strings.HasSuffix(name, "."):
		return name
	default:
		return name + "." + dns.Fqdn(zone)
	}
}

// Owner is the FQDN this record is written under.
func (r Record) Owner() string { return FQDN(r.Name, r.Zone) }

// Key uniquely identifies the RRset+value this record represents, used to diff
// desired against applied state.
func (r Record) Key() string {
	return strings.ToLower(fmt.Sprintf("%s|%s|%s|%s", dns.Fqdn(r.Zone), r.Owner(), strings.ToUpper(r.Type), r.Value))
}

// RR renders the record as a miekg/dns resource record. It returns an error for
// a malformed name/type/value (e.g. an empty label) rather than silently
// emitting broken wire data — the structured failure the shell version lacked.
func (r Record) RR() (dns.RR, error) {
	line := fmt.Sprintf("%s %d IN %s %s", r.Owner(), r.TTL, strings.ToUpper(r.Type), r.Value)
	rr, err := dns.NewRR(line)
	if err != nil {
		return nil, fmt.Errorf("record %q: %w", line, err)
	}
	if rr == nil {
		return nil, fmt.Errorf("record %q: parsed to nil", line)
	}
	return rr, nil
}

// Set is a parsed collection of desired records keyed by Key().
type Set struct {
	byKey map[string]Record
}

// NewSet builds an empty Set.
func NewSet() *Set { return &Set{byKey: map[string]Record{}} }

// Add inserts a record, validating that it renders to a well-formed RR.
func (s *Set) Add(r Record) error {
	if _, err := r.RR(); err != nil {
		return err
	}
	s.byKey[r.Key()] = r
	return nil
}

// Records returns the records in a stable order (by zone then owner then type).
func (s *Set) Records() []Record {
	out := make([]Record, 0, len(s.byKey))
	for _, r := range s.byKey {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Zone != out[j].Zone {
			return out[i].Zone < out[j].Zone
		}
		if out[i].Owner() != out[j].Owner() {
			return out[i].Owner() < out[j].Owner()
		}
		return out[i].Key() < out[j].Key()
	})
	return out
}

// Zones returns the distinct zones present, sorted.
func (s *Set) Zones() []string {
	seen := map[string]struct{}{}
	for _, r := range s.byKey {
		seen[dns.Fqdn(r.Zone)] = struct{}{}
	}
	zs := make([]string, 0, len(seen))
	for z := range seen {
		zs = append(zs, z)
	}
	sort.Strings(zs)
	return zs
}

// Has reports whether the set contains a record with the given Key.
func (s *Set) Has(key string) bool { _, ok := s.byKey[key]; return ok }

// Len returns the number of records.
func (s *Set) Len() int { return len(s.byKey) }

// Save writes the set to path (atomically) in the canonical
// zone|name|type|ttl|value format, so it can be reloaded as applied state.
func Save(set *Set, path string) error {
	var b strings.Builder
	b.WriteString("# dns-updater applied state; do not edit\n")
	for _, r := range set.Records() {
		fmt.Fprintf(&b, "%s|%s|%s|%d|%s\n", r.Zone, r.Name, r.Type, r.TTL, r.Value)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadOrEmpty parses path, returning an empty Set if the file does not exist.
func LoadOrEmpty(path string) (*Set, error) {
	s, err := Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewSet(), nil
		}
		return s, err
	}
	return s, nil
}

// Load parses the records file at path. Malformed lines are returned as an
// aggregated error but every well-formed record is still collected, so a single
// bad line does not block the rest.
func Load(path string) (*Set, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parse(f)
}

func parse(r io.Reader) (*Set, error) {
	set := NewSet()
	var errs []string
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rec, err := parseLine(line)
		if err != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", ln, err))
			continue
		}
		if err := set.Add(rec); err != nil {
			errs = append(errs, fmt.Sprintf("line %d: %v", ln, err))
		}
	}
	if err := sc.Err(); err != nil {
		return set, err
	}
	if len(errs) > 0 {
		return set, fmt.Errorf("%d bad record(s): %s", len(errs), strings.Join(errs, "; "))
	}
	return set, nil
}

func parseLine(line string) (Record, error) {
	parts := strings.SplitN(line, "|", 5)
	if len(parts) < 5 {
		return Record{}, fmt.Errorf("want zone|name|type|ttl|value, got %q", line)
	}
	ttl, err := strconv.ParseUint(strings.TrimSpace(parts[3]), 10, 32)
	if err != nil {
		return Record{}, fmt.Errorf("bad ttl %q: %w", parts[3], err)
	}
	return Record{
		Zone:  strings.TrimSpace(parts[0]),
		Name:  strings.TrimSpace(parts[1]),
		Type:  strings.TrimSpace(parts[2]),
		TTL:   uint32(ttl),
		Value: strings.TrimSpace(parts[4]),
	}, nil
}
