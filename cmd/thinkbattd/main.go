// Command thinkbattd is the battery-management daemon. It owns the resting
// charge band, runs a fuel-gauge calibration when due, and serves the thinkbatt
// control socket. The orchestration lives in internal/daemon; this command is
// flag parsing, wiring, signal handling, and sleep inhibition.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/caps"
	"git.airies.net/vifair22/thinkbatt/internal/config"
	"git.airies.net/vifair22/thinkbatt/internal/control"
	"git.airies.net/vifair22/thinkbatt/internal/daemon"
)

// Set at link time with -ldflags "-X main.version=... -X main.buildTime=...".
var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	if err := runArgs(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "thinkbattd: %v\n", err)
		os.Exit(1)
	}
}

func runArgs(args []string) error {
	fs := flag.NewFlagSet("thinkbattd", flag.ContinueOnError)
	var (
		cfgPath     = fs.String("config", "/etc/thinkbatt/thinkbatt.ini", "config file path")
		sockPath    = fs.String("socket", "/run/thinkbattd/control.sock", "control socket path")
		statePath   = fs.String("state", "/var/lib/thinkbattd/state.json", "state file path")
		batRoot     = fs.String("sysfs-root", battery.DefaultRoot, "power_supply sysfs root")
		batName     = fs.String("battery", "BAT0", "battery name under the sysfs root")
		checkEvery  = fs.Duration("check-interval", time.Hour, "how often to re-check whether calibration is due")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *showVersion {
		fmt.Printf("thinkbattd %s (built %s)\n", version, buildTime)
		return nil
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return serve(ctx, serveOpts{
		log:        log,
		cfg:        cfg,
		bat:        battery.NewSysfs(*batRoot, *batName),
		inhibitor:  sdInhibitor{log: log},
		sockPath:   *sockPath,
		statePath:  *statePath,
		checkEvery: *checkEvery,
	})
}

type serveOpts struct {
	log        *slog.Logger
	cfg        config.Config
	bat        battery.Battery
	inhibitor  daemon.Inhibitor
	sockPath   string
	statePath  string
	checkEvery time.Duration
}

// serve wires the daemon to the hardware and the control socket and runs until
// ctx is cancelled. It is separate from flag parsing so a test can drive it
// with a simulated battery and temp paths.
func serve(ctx context.Context, o serveOpts) error {
	profile, err := caps.Detect(o.bat)
	if err != nil {
		return fmt.Errorf("detect capabilities: %w", err)
	}
	o.log.Info("thinkbattd starting", "version", version, "caps", profile.Summary(),
		"band", fmt.Sprintf("%d/%d", o.cfg.BandStart, o.cfg.BandStop))
	if !profile.CanManageBand() {
		o.log.Warn("machine has no charge thresholds; band management disabled")
	}
	if !profile.CanCalibrate() {
		o.log.Warn("machine cannot force-discharge; automatic calibration disabled")
	}

	for _, dir := range []string{filepath.Dir(o.sockPath), filepath.Dir(o.statePath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	d, err := daemon.New(daemon.Options{
		Battery:   o.bat,
		Profile:   profile,
		Config:    o.cfg,
		StatePath: o.statePath,
		Inhibitor: o.inhibitor,
		Logger:    o.log,
	})
	if err != nil {
		return fmt.Errorf("init daemon: %w", err)
	}

	if profile.CanManageBand() {
		if err := d.AssertBand(); err != nil {
			o.log.Error("assert resting band at startup failed", "err", err)
		}
	}

	srv := control.NewServer(o.sockPath, d, o.log)
	ticker := time.NewTicker(o.checkEvery)
	defer ticker.Stop()

	if err := d.Serve(ctx, srv, ticker.C, time.Now); err != nil {
		return fmt.Errorf("control server: %w", err)
	}
	o.log.Info("shut down")
	return nil
}

// sdInhibitor blocks sleep and idle for the life of a held systemd-inhibit
// child. If systemd-inhibit is unavailable the Inhibit call reports the error
// and the daemon proceeds without a lock (best effort).
type sdInhibitor struct{ log *slog.Logger }

func (s sdInhibitor) Inhibit(reason string) (func(), error) {
	cmd := exec.Command("systemd-inhibit",
		"--what=sleep:idle", "--who=thinkbattd", "--why="+reason, "--mode=block",
		"sleep", "infinity")
	if err := cmd.Start(); err != nil {
		return func() {}, err
	}
	s.log.Info("sleep inhibited for calibration")
	return func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		s.log.Info("sleep inhibitor released")
	}, nil
}
