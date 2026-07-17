// Command dns-updater is a daemon that keeps a host's DNS records current on a
// BIND server. It reconciles a desired-records file to the server via
// TSIG-signed RFC2136 updates, re-running whenever the file changes, a network
// interface address changes, or a periodic timer fires.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/miekg/dns"

	"github.com/IUCLabs/dns-updater/internal/api"
	"github.com/IUCLabs/dns-updater/internal/config"
	"github.com/IUCLabs/dns-updater/internal/records"
	"github.com/IUCLabs/dns-updater/internal/tsig"
	"github.com/IUCLabs/dns-updater/internal/updater"
	"github.com/IUCLabs/dns-updater/internal/watch"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "dns-updater:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Parse(args)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel(cfg.LogLevel)}))
	log.Info("starting", "version", version, "server", cfg.Server,
		"records", cfg.RecordsFile, "watch_interfaces", cfg.WatchIface, "resync", cfg.ResyncEvery.String())

	key, err := tsig.Load(cfg.KeyFile)
	if err != nil {
		return fmt.Errorf("load key: %w", err)
	}
	log.Info("loaded tsig key", "name", strings.TrimSuffix(key.Name, "."), "algorithm", strings.TrimSuffix(key.Algorithm, "."))
	if !cfg.Filter.Empty() {
		log.Info("record filter active",
			"deny_ranges", len(cfg.Filter.DenyRanges), "allow_ranges", len(cfg.Filter.AllowRanges),
			"deny_domains", len(cfg.Filter.DenyDomains), "allow_domains", len(cfg.Filter.AllowDomains))
	}

	app := updater.New(cfg.Server, key, cfg.Timeout)
	store := api.NewStore(version, cfg.Server, cfg.RecordsFile)
	d := &daemon{cfg: cfg, app: app, log: log, store: store}

	if cfg.Oneshot {
		return d.reconcile("oneshot")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if cfg.APIAddr != "" {
		go func() {
			if err := api.Serve(ctx, cfg.APIAddr, store); err != nil {
				log.Warn("status api stopped", "err", err)
			}
		}()
		log.Info("status api listening", "addr", cfg.APIAddr)
	}

	triggers := make(chan watch.Event, 32)
	if err := watch.File(ctx, cfg.RecordsFile, triggers, log); err != nil {
		return fmt.Errorf("watch file: %w", err)
	}
	if cfg.WatchIface {
		if err := watch.Interfaces(ctx, triggers, log); err != nil {
			return fmt.Errorf("watch interfaces: %w", err)
		}
	}

	// First reconcile at startup; report readiness to systemd regardless so a
	// transient server outage does not wedge the unit in "activating".
	if err := d.reconcile("startup"); err != nil {
		log.Warn("initial reconcile failed; will retry on next trigger", "err", err)
	}
	config.SdNotify("READY=1")
	log.Info("ready")

	installHUP(ctx, triggers)
	d.loop(ctx, triggers)
	log.Info("shutting down")
	return nil
}

type daemon struct {
	cfg   *config.Config
	app   *updater.Applier
	log   *slog.Logger
	store *api.Store
}

// loop coalesces triggers with a debounce and runs a periodic resync.
func (d *daemon) loop(ctx context.Context, triggers <-chan watch.Event) {
	var resync <-chan time.Time
	if d.cfg.ResyncEvery > 0 {
		t := time.NewTicker(d.cfg.ResyncEvery)
		defer t.Stop()
		resync = t.C
	}

	var timer *time.Timer
	var reason string
	fire := make(chan struct{}, 1)
	arm := func(src string) {
		d.log.Debug("trigger", "source", src)
		reason = src
		if timer == nil {
			timer = time.AfterFunc(d.cfg.Debounce, func() {
				select {
				case fire <- struct{}{}:
				default:
				}
			})
		} else {
			timer.Reset(d.cfg.Debounce)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-triggers:
			arm(ev.Source + ":" + ev.Detail)
		case <-resync:
			arm("resync")
		case <-fire:
			if reason == "" {
				continue
			}
			r := reason
			reason = ""
			if err := d.reconcile(r); err != nil {
				d.log.Warn("reconcile failed", "trigger", r, "err", err)
			}
		}
	}
}

// reconcile loads desired + applied state, pushes the delta, persists the new
// applied state, and updates the status store. Logging is intentionally quiet:
// INFO only when something actually changes or fails; the steady-state
// "nothing to do" path stays at DEBUG so periodic resyncs and interface flaps
// do not spam the journal.
func (d *daemon) reconcile(trigger string) error {
	desired, err := records.Load(d.cfg.RecordsFile)
	if err != nil && desired.Len() == 0 {
		d.setStatus(trigger, nil, desired.Len(), err)
		return fmt.Errorf("load records: %w", err)
	}
	if err != nil {
		d.log.Warn("some records skipped", "trigger", trigger, "err", err)
	}
	if !d.cfg.Filter.Empty() {
		var dropped int
		desired, dropped = d.cfg.Filter.Apply(desired)
		if dropped > 0 {
			d.log.Debug("filtered records", "trigger", trigger, "dropped", dropped, "kept", desired.Len())
		}
	}
	applied, aerr := records.LoadOrEmpty(d.cfg.StateFile)
	if aerr != nil {
		d.log.Warn("could not read applied state; assuming empty", "err", aerr)
		applied = records.NewSet()
	}

	res := d.app.Reconcile(desired, applied)
	changed := res.String() != "no changes"

	if changed {
		newApplied := res.Applied(desired, applied)
		if err := records.Save(newApplied, d.cfg.StateFile); err != nil {
			d.log.Warn("could not persist applied state", "err", err)
		}
	}
	d.setStatus(trigger, &res, desired.Len(), nil)

	switch {
	case !res.OK():
		d.log.Warn("reconcile partial", "trigger", trigger, "result", res.String())
		for _, z := range res.Zones {
			if !z.OK() {
				d.log.Warn("zone update failed", "zone", strings.TrimSuffix(z.Zone, "."),
					"rcode", dns.RcodeToString[z.Rcode], "err", z.Err)
			}
		}
		return fmt.Errorf("one or more zones failed: %s", res.String())
	case changed:
		d.log.Info("applied", "trigger", trigger, "result", res.String(), "managed", desired.Len())
	default:
		d.log.Debug("no changes", "trigger", trigger, "managed", desired.Len())
	}
	return nil
}

// setStatus mirrors the reconcile outcome into the API store.
func (d *daemon) setStatus(trigger string, res *updater.Result, managed int, loadErr error) {
	prev := d.store.Get()
	s := api.Status{
		Healthy:        loadErr == nil && (res == nil || res.OK()),
		ManagedRecords: managed,
		LastReconcile:  time.Now(),
		LastChange:     prev.LastChange,
	}
	if loadErr != nil {
		s.LastError = loadErr.Error()
	}
	if res != nil {
		if res.String() != "no changes" {
			s.LastChange = time.Now()
		}
		for _, z := range res.Zones {
			zs := api.ZoneStatus{
				Zone: strings.TrimSuffix(z.Zone, "."), Adds: z.Adds, Deletes: z.Deletes,
				Rcode: z.Rcode, RcodeText: dns.RcodeToString[z.Rcode],
			}
			if z.Err != nil {
				zs.Error = z.Err.Error()
				if s.LastError == "" {
					s.LastError = z.Err.Error()
				}
			}
			s.Zones = append(s.Zones, zs)
		}
	}
	d.store.Set(s)
}

func installHUP(ctx context.Context, triggers chan<- watch.Event) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-hup:
				select {
				case triggers <- watch.Event{Source: "signal", Detail: "SIGHUP"}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
}

func logLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
