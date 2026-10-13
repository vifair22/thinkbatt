package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/control"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// stubHandler is a control.Handler that records calls and never errors.
type stubHandler struct {
	calibCalled bool
	bandStart   int
	bandStop    int
}

func (h *stubHandler) Status() (control.StatusInfo, error) {
	return control.StatusInfo{Packs: []control.PackStatus{{
		Name: "BAT0", Capacity: 79, Status: "Not charging", BehaviourActive: "auto",
		BandStart: 75, BandStop: 80, HasThresholds: true,
		EnergyFullUWh: 112_700_000, EnergyFullDesignUWh: 94_000_000,
		CapsSummary: "band=yes calibrate=yes",
		LastCalibration: &state.Calibration{
			FinishedAt: time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC),
			Outcome:    state.OutcomeOK, Reason: "learning cycle complete",
			EnergyFullBeforeUWh: 112_700_000, EnergyFullAfterUWh: 94_000_000,
		},
	}}}, nil
}
func (h *stubHandler) Calibrate(string, bool) error { h.calibCalled = true; return nil }
func (h *stubHandler) Cancel() error                { return nil }
func (h *stubHandler) SetBand(battery string, start, stop int) error {
	h.bandStart, h.bandStop = start, stop
	return nil
}

func serveStub(t *testing.T, h control.Handler) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ctl.sock")
	srv := control.NewServer(path, "", h, nil)
	go srv.Serve() //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	return path
}

func TestCLIAgainstServer(t *testing.T) {
	h := &stubHandler{}
	path := serveStub(t, h)
	t.Setenv("THINKBATT_SOCKET", path)

	ok := [][]string{
		{"status"},
		{"calibrate", "--now"},
		{"cancel"},
		{"band", "70", "85"},
		{"version"},
		{"help"},
	}
	for _, args := range ok {
		if err := run(args); err != nil {
			t.Errorf("run(%v): %v", args, err)
		}
	}
	if !h.calibCalled {
		t.Errorf("calibrate not forwarded to the daemon")
	}
	if h.bandStart != 70 || h.bandStop != 85 {
		t.Errorf("band not forwarded: %d/%d", h.bandStart, h.bandStop)
	}
}

func TestCLIErrors(t *testing.T) {
	// No socket: commands that talk to the daemon must error.
	t.Setenv("THINKBATT_SOCKET", filepath.Join(t.TempDir(), "absent.sock"))
	bad := [][]string{
		{},                 // no command
		{"bogus"},          // unknown command
		{"band", "x", "y"}, // non-integer args (caught before dialing)
		{"band", "70"},     // wrong arg count
		{"status"},         // dial failure
	}
	for _, args := range bad {
		if err := run(args); err == nil {
			t.Errorf("run(%v) should have errored", args)
		}
	}
}
