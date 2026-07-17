// Package tsig loads a BIND-style TSIG key file for signing RFC2136 updates.
//
// The file looks like:
//
//	key "client-update" {
//	    algorithm hmac-sha256;
//	    secret "base64secret==";
//	};
package tsig

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/miekg/dns"
)

// Key is a parsed TSIG key ready for use with miekg/dns.
type Key struct {
	Name      string // fully qualified (trailing dot), as miekg/dns wants
	Algorithm string // e.g. dns.HmacSHA256 ("hmac-sha256.")
	Secret    string // base64
}

var (
	reName = regexp.MustCompile(`(?s)key\s+"([^"]+)"\s*\{(.*?)\}`)
	reAlgo = regexp.MustCompile(`algorithm\s+([A-Za-z0-9\-]+)\s*;`)
	reSec  = regexp.MustCompile(`secret\s+"([^"]+)"\s*;`)
)

// algorithms maps BIND algorithm names to the fully-qualified constants
// miekg/dns expects in SetTsig and the TsigSecret map.
var algorithms = map[string]string{
	"hmac-md5":    dns.HmacMD5,
	"hmac-sha1":   dns.HmacSHA1,
	"hmac-sha224": dns.HmacSHA224,
	"hmac-sha256": dns.HmacSHA256,
	"hmac-sha384": dns.HmacSHA384,
	"hmac-sha512": dns.HmacSHA512,
}

// Load reads and parses the first key definition in the file at path.
func Load(path string) (*Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(string(b))
}

// Parse extracts a Key from BIND key-file text.
func Parse(text string) (*Key, error) {
	m := reName.FindStringSubmatch(text)
	if m == nil {
		return nil, fmt.Errorf("no key { ... } block found")
	}
	name, body := m[1], m[2]

	algoM := reAlgo.FindStringSubmatch(body)
	if algoM == nil {
		return nil, fmt.Errorf("key %q: missing algorithm", name)
	}
	algo, ok := algorithms[strings.ToLower(algoM[1])]
	if !ok {
		return nil, fmt.Errorf("key %q: unsupported algorithm %q", name, algoM[1])
	}

	secM := reSec.FindStringSubmatch(body)
	if secM == nil {
		return nil, fmt.Errorf("key %q: missing secret", name)
	}

	return &Key{
		Name:      dns.Fqdn(name),
		Algorithm: algo,
		Secret:    secM[1],
	}, nil
}

// SecretMap returns the name→secret map for dns.Client.TsigSecret.
func (k *Key) SecretMap() map[string]string {
	return map[string]string{k.Name: k.Secret}
}
