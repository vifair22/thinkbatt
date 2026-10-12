package main

import (
	"context"
	"log/slog"
	"os"
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
			cfg:        config.Default(),
			bat:        sim,
			sockPath:   sock,
			statePath:  filepath.Join(dir, "state.json"),
			checkEvery: time.Hour,
		})
	}()

	// Once the socket exists, startup wiring (including AssertBand) has run.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
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
