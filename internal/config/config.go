// Package config holds dns-updater's runtime configuration, sourced from flags
// with environment-variable fallbacks so it works as a systemd unit with an
// EnvironmentFile.
package config

import (
	"flag"
	"fmt"
	"os"
	"time"
)

// Config is the daemon configuration.
type Config struct {
	Server      string        // authoritative write endpoint host[:port]
	KeyFile     string        // BIND-style TSIG key file
	RecordsFile string        // desired records (zone|name|type|ttl|value)
	StateFile   string        // last-applied state
	ResyncEvery time.Duration // periodic reconcile safety net
	Debounce    time.Duration // coalesce bursts of watch events
	Timeout     time.Duration // per-update network timeout
	WatchIface  bool          // subscribe to interface address changes
	Oneshot     bool          // reconcile once and exit (no watching)
	APIAddr     string        // status API address (unix socket path or host:port; empty disables)
	LogLevel    string        // debug|info|warn|error
}

const defaultPort = "53"

// Parse builds a Config from args (typically os.Args[1:]).
func Parse(args []string) (*Config, error) {
	fs := flag.NewFlagSet("dns-updater", flag.ContinueOnError)
	c := &Config{}
	fs.StringVar(&c.Server, "server", env("DNS_UPDATER_SERVER", ""), "authoritative write endpoint host[:port]")
	fs.StringVar(&c.KeyFile, "key-file", env("DNS_UPDATER_KEY_FILE", "/etc/dns-updater/key"), "BIND-style TSIG key file")
	fs.StringVar(&c.RecordsFile, "records-file", env("DNS_UPDATER_RECORDS_FILE", "/var/lib/dns-updater/records"), "desired records file")
	fs.StringVar(&c.StateFile, "state-file", env("DNS_UPDATER_STATE_FILE", "/var/lib/dns-updater/applied"), "last-applied state file")
	fs.DurationVar(&c.ResyncEvery, "resync", envDur("DNS_UPDATER_RESYNC", 10*time.Minute), "periodic reconcile interval (0 disables)")
	fs.DurationVar(&c.Debounce, "debounce", envDur("DNS_UPDATER_DEBOUNCE", 2*time.Second), "coalesce watch events for this long")
	fs.DurationVar(&c.Timeout, "timeout", envDur("DNS_UPDATER_TIMEOUT", 10*time.Second), "per-update network timeout")
	fs.BoolVar(&c.WatchIface, "watch-interfaces", envBool("DNS_UPDATER_WATCH_INTERFACES", true), "reconcile on interface address changes")
	fs.BoolVar(&c.Oneshot, "oneshot", envBool("DNS_UPDATER_ONESHOT", false), "reconcile once and exit")
	fs.StringVar(&c.APIAddr, "api", env("DNS_UPDATER_API", "/run/dns-updater/api.sock"), "status API address (unix path or host:port; empty disables)")
	fs.StringVar(&c.LogLevel, "log-level", env("DNS_UPDATER_LOG_LEVEL", "info"), "log level: debug|info|warn|error")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.Server == "" {
		return nil, fmt.Errorf("server is required (-server or DNS_UPDATER_SERVER)")
	}
	c.Server = withPort(c.Server)
	return c, nil
}

func withPort(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s // already has a port
		}
		if s[i] == ']' {
			break // IPv6 literal without port
		}
	}
	return s + ":" + defaultPort
}

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok {
		return v
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok {
		return v == "1" || v == "true" || v == "yes"
	}
	return def
}
