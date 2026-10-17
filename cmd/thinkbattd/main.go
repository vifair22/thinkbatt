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
	"git.airies.net/vifair22/thinkbatt/internal/calib"
	"git.airies.net/vifair22/thinkbatt/internal/config"
	"git.airies.net/vifair22/thinkbatt/internal/control"
	"git.airies.net/vifair22/thinkbatt/internal/daemon"
	"git.airies.net/vifair22/thinkbatt/internal/load"
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
		maintEvery  = fs.Duration("maintain-interval", 2*time.Minute, "how often to rescan packs, re-assert bands, and check whether a calibration is due")
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

	cfgFile, err := config.LoadFile(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return serve(ctx, serveOpts{
		log:        log,
		cfgFile:    cfgFile,
		set:        battery.NewSysfsSet(*batRoot),
		inhibitor:  sdInhibitor{log: log},
		sockPath:   *sockPath,
		statePath:  *statePath,
		checkEvery: *maintEvery,
	})
}

// sampleEvery is how often the daemon samples pack power for the time
// estimates' moving average: a handful of samples per time constant.
const sampleEvery = 10 * time.Second

type serveOpts struct {
	log        *slog.Logger
	cfgFile    config.File
	set        battery.Set
	inhibitor  daemon.Inhibitor
	sockPath   string
	statePath  string
	checkEvery time.Duration
}

// serve wires the daemon to the hardware and the control socket and runs until
// ctx is cancelled. It is separate from flag parsing so a test can drive it
// with simulated packs and temp paths.
func serve(ctx context.Context, o serveOpts) error {
	o.log.Info("thinkbattd starting", "version", version,
		"band", fmt.Sprintf("%d/%d", o.cfgFile.Global.BandStart, o.cfgFile.Global.BandStop),
		"tuned_packs", o.cfgFile.OverriddenPacks())

	for _, dir := range []string{filepath.Dir(o.sockPath), filepath.Dir(o.statePath)} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}

	// assistFactory binds a discharge assist to whichever pack a calibration
	// runs on: it holds the force-discharge leg at the configured C-rate with a
	// controlled CPU load, reading that pack's power_now as feedback. The daemon
	// calls it per run, only for packs that can force-discharge.
	assistFactory := func(bat battery.Battery) calib.DischargeAssist {
		measure := func() (float64, error) {
			r, err := bat.Read()
			if err != nil {
				return 0, err
			}
			return float64(r.PowerNowUW) / 1e6, nil
		}
		return load.NewAssist(0, measure, load.RealClock{}, o.log)
	}

	d, err := daemon.New(daemon.Options{
		Set:       o.set,
		File:      o.cfgFile,
		StatePath: o.statePath,
		Inhibitor: o.inhibitor,
		Assist:    assistFactory,
		Logger:    o.log,
	})
	if err != nil {
		return fmt.Errorf("init daemon: %w", err)
	}

	srv := control.NewServer(o.sockPath, o.cfgFile.Global.SocketGroup, d, o.log)
	ticker := time.NewTicker(o.checkEvery)
	defer ticker.Stop()
	sampler := time.NewTicker(sampleEvery)
	defer sampler.Stop()

	if err := d.Serve(ctx, srv, ticker.C, sampler.C, time.Now); err != nil {
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
