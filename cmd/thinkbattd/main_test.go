package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/config"
)

func TestVersionFlag(t *testing.T) {
	// -version parses flags, prints, and returns without touching hardware.
	if err := runArgs([]string{"-version"}); err != nil {
		t.Errorf("run -version: %v", err)
	}
}

func TestBadFlag(t *testing.T) {
	if err := runArgs([]string{"-nope"}); err == nil {
		t.Errorf("unknown flag should error")
	}
}

func TestInhibitor(t *testing.T) {
	inh := sdInhibitor{log: slog.New(slog.DiscardHandler)}
	release, err := inh.Inhibit("test")
	// Either systemd-inhibit ran (release it) or it was unavailable (error
	// path). Both are valid; this exercises the code either way.
	if err == nil {
		release()
	}
}

// oneSet is a battery.Set over a single in-memory pack for the wiring test.
type oneSet struct {
	name string
	bat  battery.Battery
}

func (s oneSet) List() ([]string, error)     { return []string{s.name}, nil }
func (s oneSet) Open(string) battery.Battery { return s.bat }

func TestServeWiring(t *testing.T) {
	dir := t.TempDir()
	// Band-capable but not calibrate-capable, so the startup due-check does not
	// launch a real calibration during the test.
	sim := battery.NewSim(battery.SimConfig{
		Capacity: 79, Start: 10, Stop: 20, HasThresholds: true, ACOnline: true,
		Supported: []battery.Behaviour{battery.BehaviourAuto},
	})
	sock := filepath.Join(dir, "ctl.sock")

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- serve(ctx, serveOpts{
			log:        slog.New(slog.DiscardHandler),
			cfgFile:    config.File{Global: config.Default()},
			set:        oneSet{name: "BAT0", bat: sim},
			sockPath:   sock,
			statePath:  filepath.Join(dir, "state.json"),
			checkEvery: time.Hour,
		})
	}()

	// The startup maintenance pass asserts the resting band; poll for it, since
	// it runs concurrently with the control socket coming up.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if start, stop, err := sim.Thresholds(); err == nil && start == 75 && stop == 80 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	start, stop, err := sim.Thresholds()
	if err != nil {
		t.Fatal(err)
	}
	if start != 75 || stop != 80 {
		t.Errorf("startup did not assert the resting band: %d/%d", start, stop)
	}

	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("serve: %v", err)
	}
}
