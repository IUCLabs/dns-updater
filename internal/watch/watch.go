// Package watch turns records-file changes and network-interface address
// changes into reconcile triggers on a single channel.
package watch

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/fsnotify/fsnotify"
	"github.com/vishvananda/netlink"
)

// Event describes why a reconcile was triggered.
type Event struct {
	Source string // "file", "iface", or "startup"
	Detail string
}

// File watches the records file for changes. It watches the containing
// directory (not the file inode) so that atomic replace — the write-temp +
// rename pattern puppet/concat uses — is still detected. Events are debounced by
// the caller.
func File(ctx context.Context, path string, out chan<- Event, log *slog.Logger) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	if err := w.Add(dir); err != nil {
		w.Close()
		return err
	}
	log.Info("watching records file", "path", path)

	go func() {
		defer w.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) != base {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) == 0 {
					continue
				}
				send(ctx, out, Event{Source: "file", Detail: ev.Op.String()})
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Warn("file watcher error", "err", err)
			}
		}
	}()
	return nil
}

// Interfaces watches for new interface addresses and emits a trigger, so a DHCP
// assignment/renew re-asserts the host's records without waiting for the next
// file write.
//
// It deliberately reacts ONLY to address ADDITIONS, not removals: an interface
// going down (address removed) is usually transient and must not disturb
// records — we act only when a new IP appears (an actual change). Loopback and
// link-local addresses are ignored as noise.
func Interfaces(ctx context.Context, out chan<- Event, log *slog.Logger) error {
	updates := make(chan netlink.AddrUpdate, 16)
	done := make(chan struct{})
	if err := netlink.AddrSubscribe(updates, done); err != nil {
		return err
	}
	log.Info("watching network interface addresses")

	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case u, ok := <-updates:
				if !ok {
					return
				}
				if !u.NewAddr {
					continue // address removed / interface down — ignore
				}
				// Trigger only for real routable addresses. IsGlobalUnicast is
				// true for normal v4/v6 addresses on ANY device type — including
				// dummy/anycast interfaces (the address is classified by value,
				// not by the device) — and false for loopback, link-local,
				// multicast and unspecified, which are never record targets.
				ip := u.LinkAddress.IP
				if !ip.IsGlobalUnicast() {
					continue
				}
				send(ctx, out, Event{Source: "iface", Detail: "new address " + ip.String()})
			}
		}
	}()
	return nil
}

func send(ctx context.Context, out chan<- Event, ev Event) {
	select {
	case out <- ev:
	case <-ctx.Done():
	}
}
