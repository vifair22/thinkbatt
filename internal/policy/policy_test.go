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

func TestOverrideBand(t *testing.T) {
	if start, stop := OverrideBand(100); start != 99 || stop != 100 {
		t.Errorf("OverrideBand(100) = %d/%d, want 99/100", start, stop)
	}
	if start, stop := OverrideBand(90); start != 89 || stop != 90 {
		t.Errorf("OverrideBand(90) = %d/%d, want 89/90", start, stop)
	}
}

func TestOverrideEnded(t *testing.T) {
	set := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	deadline := state.Override{Target: 100, Until: set.Add(time.Hour), SetAt: set}
	full := state.Override{Target: 100, UntilFull: true, SetAt: set}
	settled := set.Add(5 * time.Minute)
	cases := []struct {
		name string
		o    state.Override
		r    battery.Reading
		now  time.Time
		want bool
	}{
		{"deadline not yet", deadline, battery.Reading{}, set.Add(59 * time.Minute), false},
		{"deadline reached", deadline, battery.Reading{}, set.Add(time.Hour), true},
		{"full: reached target", full, battery.Reading{Capacity: 100, Status: battery.StatusFull}, set, true},
		{"full: still charging", full, battery.Reading{Capacity: 98, Status: battery.StatusCharging, ACOnline: true}, settled, false},
		{"full: charger stopped near target", full, battery.Reading{Capacity: 98, Status: battery.StatusNotCharging, ACOnline: true}, settled, true},
		{"full: stopped near target before settle", full, battery.Reading{Capacity: 98, Status: battery.StatusNotCharging, ACOnline: true}, set.Add(time.Minute), false},
		{"full: stopped well below target", full, battery.Reading{Capacity: 80, Status: battery.StatusNotCharging, ACOnline: true}, settled, false},
		{"full: on battery near target", full, battery.Reading{Capacity: 98, Status: battery.StatusDischarging}, settled, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := OverrideEnded(c.o, c.r, c.now)
			if got != c.want {
				t.Errorf("OverrideEnded = %t (%s), want %t", got, why, c.want)
			}
			if got && why == "" {
				t.Errorf("ended without a reason")
			}
		})
	}
}
