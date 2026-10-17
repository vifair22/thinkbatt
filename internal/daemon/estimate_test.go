package daemon

import (
	"testing"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/calib"
)

func TestPowerAverage(t *testing.T) {
	t0 := time.Unix(1000, 0)
	charging := func(w float64) battery.Reading {
		return battery.Reading{Status: battery.StatusCharging, PowerNowUW: int64(w * 1e6)}
	}
	discharging := func(w float64) battery.Reading {
		return battery.Reading{Status: battery.StatusDischarging, PowerNowUW: int64(w * 1e6)}
	}

	var a powerAverage
	if got := a.powerFor(charging(11)); got != 11e6 {
		t.Fatalf("empty average should pass the reading through, got %v", got)
	}

	a.add(charging(10), t0)
	if a.uw != 10e6 {
		t.Fatalf("first sample seeds the average, got %v", a.uw)
	}
	// One time constant later the average has closed 1-1/e of the gap.
	a.add(charging(20), t0.Add(powerTau))
	if want := 10e6 + 10e6*(1-1/2.718281828); a.uw < want-1e3 || a.uw > want+1e3 {
		t.Errorf("after one tau: got %v, want about %v", a.uw, want)
	}
	if got := a.powerFor(charging(99)); got != a.uw {
		t.Errorf("same status should use the average, got %v", got)
	}

	// A direction change restarts it rather than blending the two rates.
	a.add(discharging(5), t0.Add(2*powerTau))
	if a.uw != -5e6 || a.status != battery.StatusDischarging {
		t.Errorf("status change should restart: %+v", a)
	}
	if got := a.powerFor(charging(7)); got != 7e6 {
		t.Errorf("a reading in another status should not use the average, got %v", got)
	}

	// A clock stepping backwards restarts too.
	a.add(discharging(9), t0)
	if a.uw != -9e6 {
		t.Errorf("backwards clock should restart, got %v", a.uw)
	}

	// Hours suspended: the stale average is all but replaced.
	a.add(discharging(3), t0.Add(3*time.Hour))
	if a.uw < -3.001e6 || a.uw > -2.999e6 {
		t.Errorf("after a long gap the new reading should dominate, got %v", a.uw)
	}
}

func TestChargeTarget(t *testing.T) {
	for _, tc := range []struct {
		name        string
		thresholds  bool
		stop        int
		calibrating bool
		phase       string
		want        int
	}{
		{"band", true, 80, false, "", 80},
		{"no thresholds", false, 80, false, "", 100},
		{"calibration charge", true, 80, true, calib.PhaseCharging, 100},
		{"calibration recharge", true, 80, true, calib.PhaseRecharging, 100},
		{"calibration discharge", true, 80, true, calib.PhaseDischarging, 0},
		{"calibration drain", true, 80, true, calib.PhaseDrainingBand, 0},
	} {
		if got := chargeTarget(tc.thresholds, tc.stop, tc.calibrating, tc.phase); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestEstimate(t *testing.T) {
	full := int64(56_800_000) // 56.8 Wh
	at := func(st battery.Status, pct float64) battery.Reading {
		return battery.Reading{Status: st, EnergyFullUWh: full, EnergyNowUWh: int64(float64(full) * pct / 100)}
	}
	for _, tc := range []struct {
		name        string
		r           battery.Reading
		powerUW     float64
		target      int
		calibrating bool
		want        estimates
	}{
		// 35% of 56.8 Wh is 19.88 Wh; at 11.36 W that is 1.75 h.
		{"charge to band", at(battery.StatusCharging, 45), 11.36e6, 80, false,
			estimates{toStop: 105 * time.Minute}},
		{"already past target", at(battery.StatusCharging, 81), 11e6, 80, false, estimates{}},
		{"charging, no target", at(battery.StatusCharging, 45), 11e6, 0, true, estimates{}},
		{"charging trickle", at(battery.StatusCharging, 79), 50_000, 80, false, estimates{}},
		// 28.4 Wh at 7.1 W is 4 h.
		{"discharge", at(battery.StatusDischarging, 50), -7.1e6, 80, false,
			estimates{toEmpty: 4 * time.Hour}},
		{"discharge in calibration", at(battery.StatusDischarging, 50), -7.1e6, 0, true, estimates{}},
		{"discharge near idle", at(battery.StatusDischarging, 50), -10_000, 80, false, estimates{}},
		{"beyond the cap", at(battery.StatusDischarging, 100), -200_000, 80, false, estimates{}},
		{"not charging", at(battery.StatusNotCharging, 80), 0, 80, false, estimates{}},
	} {
		if got := estimate(tc.r, tc.powerUW, tc.target, tc.calibrating); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// TestStatusEstimates runs the sampler against a simulated pack and checks
// Status reports the band's stop and a charge estimate, then a runtime once
// the pack is unplugged.
func TestStatusEstimates(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{
		Capacity: 40, Start: 75, Stop: 80, HasThresholds: true, ACOnline: true,
		EnergyFullUWh: 50_000_000, EnergyFullDesignUWh: 50_000_000,
		ChargeRatePctPerHr: 20, DischargeRatePctPerHr: 10,
	})
	d := newDaemon(t, singlePack(sim), nil, nil)
	t0 := time.Unix(0, 0)
	// Only the band, not Maintain: a pack never calibrated is due, and a
	// calibration would own the estimates.
	d.assertBands()
	sim.Advance(0) // the sim latches charging on as it steps

	d.Sample(t0)
	d.Sample(t0.Add(10 * time.Second))
	info, err := d.Status()
	if err != nil {
		t.Fatal(err)
	}
	p := info.Packs[0]
	// 40 points at 20 points an hour.
	if p.ChargeTarget != 80 || p.TimeToStopS != 2*3600 || p.TimeToEmptyS != 0 {
		t.Errorf("charging: target %d, to stop %ds, to empty %ds", p.ChargeTarget, p.TimeToStopS, p.TimeToEmptyS)
	}

	sim.SetAC(false)
	info, err = d.Status() // before the sampler sees the change: the raw reading
	if err != nil {
		t.Fatal(err)
	}
	// 40% at 10 points an hour.
	if p := info.Packs[0]; p.TimeToStopS != 0 || p.TimeToEmptyS != 4*3600 {
		t.Errorf("discharging: to stop %ds, to empty %ds", p.TimeToStopS, p.TimeToEmptyS)
	}
}

// TestSampleSkipsReadErrors checks a failed read leaves the average unseeded,
// so Status falls back to the raw reading rather than a zero.
func TestSampleSkipsReadErrors(t *testing.T) {
	sim := fullSetSim(50)
	d := newDaemon(t, singlePack(errBat{sim}), nil, nil)
	d.Sample(time.Unix(0, 0))
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.packs["BAT0"].power.valid {
		t.Errorf("a failed read should not seed the average")
	}
}
