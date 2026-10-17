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
	ovTarget    int
	ovUntil     time.Time
	ovFull      bool
	clearCalled bool
}

func (h *stubHandler) Status() (control.StatusInfo, error) {
	return control.StatusInfo{Packs: []control.PackStatus{{
		Name: "BAT0", Capacity: 79, Status: "Not charging", BehaviourActive: "auto",
		BandStart: 75, BandStop: 80, HasThresholds: true,
		CalibrationRunning: true, CalibrationPhase: "discharging",
		Override:      &state.Override{Target: 100, Until: time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC)},
		EnergyFullUWh: 112_700_000, EnergyFullDesignUWh: 94_000_000,
		CapsSummary: "band=yes calibrate=yes", ChargeTarget: 80, TimeToStopS: 6600,
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

func (h *stubHandler) SetOverride(_ string, target int, until time.Time, untilFull bool) error {
	h.ovTarget, h.ovUntil, h.ovFull = target, until, untilFull
	return nil
}
func (h *stubHandler) ClearOverride(string) error { h.clearCalled = true; return nil }

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
		{"status", "--json"},
		{"charge", "reset"},
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
		{},                                    // no command
		{"bogus"},                             // unknown command
		{"band", "x", "y"},                    // non-integer args (caught before dialing)
		{"band", "70"},                        // wrong arg count
		{"status"},                            // dial failure
		{"status", "x"},                       // stray positional
		{"charge"},                            // no target
		{"charge", "lots"},                    // bad target
		{"charge", "full", "--until", "soon"}, // bad deadline
		{"charge", "full", "--until", "2020-01-01 09:00"}, // past deadline
		{"charge", "full", "--bogus"},                     // unknown flag
		{"charge", "reset", "BAT0"},                       // positional instead of --battery
		{"charge", "reset", "--bogus"},                    // unknown flag
		{"charge", "full"},                                // dial failure
		{"charge", "reset"},                               // dial failure
	}
	for _, args := range bad {
		if err := run(args); err == nil {
			t.Errorf("run(%v) should have errored", args)
		}
	}
}

func TestCLICharge(t *testing.T) {
	h := &stubHandler{}
	t.Setenv("THINKBATT_SOCKET", serveStub(t, h))
	ref := time.Date(2026, 10, 16, 15, 0, 0, 0, time.Local)
	now = func() time.Time { return ref }
	t.Cleanup(func() { now = time.Now })

	if err := run([]string{"charge", "full"}); err != nil {
		t.Fatalf("charge full: %v", err)
	}
	if h.ovTarget != 100 || !h.ovFull {
		t.Errorf("charge full sent %d full=%t, want 100 until full", h.ovTarget, h.ovFull)
	}
	// Flags after the target, and a bare time that has passed today -> tomorrow.
	if err := run([]string{"charge", "90%", "--until", "09:00", "--battery", "BAT0"}); err != nil {
		t.Fatalf("charge 90 --until 09:00: %v", err)
	}
	want := time.Date(2026, 10, 17, 9, 0, 0, 0, time.Local)
	if h.ovTarget != 90 || h.ovFull || !h.ovUntil.Equal(want) {
		t.Errorf("sent %d full=%t until=%s, want 90 until %s", h.ovTarget, h.ovFull, h.ovUntil, want)
	}
}

func TestParseUntil(t *testing.T) {
	ref := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		in   string
		want time.Time
		ok   bool
	}{
		{"18:30", time.Date(2026, 10, 16, 18, 30, 0, 0, time.UTC), true},
		{"09:00", time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC), true},
		{"15:00", time.Date(2026, 10, 17, 15, 0, 0, 0, time.UTC), true}, // now is not "later today"
		{"2026-10-18 09:00", time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC), true},
		{"2026-10-18T09:00", time.Date(2026, 10, 18, 9, 0, 0, 0, time.UTC), true},
		{"2026-10-16 14:00", time.Time{}, false},
		{"25:00", time.Time{}, false},
		{"tomorrow", time.Time{}, false},
	}
	for _, c := range cases {
		got, err := parseUntil(c.in, ref)
		if (err == nil) != c.ok {
			t.Errorf("parseUntil(%q) err = %v, want ok=%t", c.in, err, c.ok)
			continue
		}
		if c.ok && !got.Equal(c.want) {
			t.Errorf("parseUntil(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestDescribePower(t *testing.T) {
	for uw, want := range map[int64]string{6_800_000: "6.8 W in", -12_340_000: "12.3 W out", 0: "0.0 W"} {
		if got := describePower(uw); got != want {
			t.Errorf("describePower(%d) = %q, want %q", uw, got, want)
		}
	}
}

func TestDescribeEstimate(t *testing.T) {
	for _, tc := range []struct {
		p    control.PackStatus
		want string
	}{
		{control.PackStatus{ChargeTarget: 80, TimeToStopS: 6600}, "80% in 1h 50m"},
		{control.PackStatus{ChargeTarget: 100, TimeToStopS: 720}, "100% in 12m"},
		{control.PackStatus{TimeToEmptyS: 11_430}, "empty in 3h 11m"},
		{control.PackStatus{TimeToEmptyS: 20}, "empty in under a minute"},
		{control.PackStatus{ChargeTarget: 80}, ""},
	} {
		if got := describeEstimate(tc.p); got != tc.want {
			t.Errorf("describeEstimate(%+v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}
