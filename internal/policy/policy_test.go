package policy

import (
	"testing"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

func TestDue(t *testing.T) {
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		last     *state.Calibration
		cycleNow int
		wantDue  bool
	}{
		{"never calibrated", nil, 10, true},
		{
			"cycles reached",
			&state.Calibration{CycleCountAt: 0, FinishedAt: now.AddDate(0, 0, -1)},
			30, true,
		},
		{
			"days reached",
			&state.Calibration{CycleCountAt: 10, FinishedAt: now.AddDate(0, 0, -91)},
			10, true,
		},
		{
			"neither reached",
			&state.Calibration{CycleCountAt: 10, FinishedAt: now.AddDate(0, 0, -10)},
			12, false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := battery.Reading{CycleCount: c.cycleNow}
			due, reason := Due(c.last, r, now, 30, 90)
			if due != c.wantDue {
				t.Errorf("Due = %t (%s), want %t", due, reason, c.wantDue)
			}
		})
	}
}

func TestAssertBand(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{Capacity: 50, HasThresholds: true, Start: 10, Stop: 20})
	if err := AssertBand(sim, 75, 80); err != nil {
		t.Fatalf("AssertBand: %v", err)
	}
	start, stop, err := sim.Thresholds()
	if err != nil {
		t.Fatal(err)
	}
	if start != 75 || stop != 80 {
		t.Errorf("band = %d/%d, want 75/80", start, stop)
	}
	b, _ := sim.Behaviour()
	if b != battery.BehaviourAuto {
		t.Errorf("behaviour = %q, want auto", b)
	}
}

func TestAssertBandNoThresholds(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{Capacity: 50, HasThresholds: false})
	if err := AssertBand(sim, 75, 80); err != nil {
		t.Fatalf("AssertBand must tolerate no thresholds: %v", err)
	}
	if b, _ := sim.Behaviour(); b != battery.BehaviourAuto {
		t.Errorf("behaviour = %q, want auto", b)
	}
}
