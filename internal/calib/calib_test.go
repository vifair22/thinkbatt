package calib

import (
	"context"
	"strings"
	"testing"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// fakeClock drives virtual time and advances the simulated battery on each
// Sleep, so a multi-hour calibration runs in microseconds and deterministically.
// after, if set, runs after each advance and lets a test perturb the run (drop
// AC, cancel the context).
type fakeClock struct {
	now   time.Time
	sim   *battery.Sim
	after func()
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	c.sim.Advance(d)
	if c.after != nil {
		c.after()
	}
	return nil
}

func fullSetSim(cap int) *battery.Sim {
	return battery.NewSim(battery.SimConfig{
		Capacity:            cap,
		Start:               75,
		Stop:                80,
		HasThresholds:       true,
		ACOnline:            true,
		EnergyFullUWh:       112_700_000,
		EnergyFullDesignUWh: 94_000_000,
		RelearnFloor:        10,
	})
}

func stdParams() Params {
	return Params{BandStart: 75, BandStop: 80, Floor: 7, PollInterval: 30 * time.Second}
}

func TestFullCycleRelearn(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	e := New(sim, clk, nil, stdParams())

	res, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeOK {
		t.Fatalf("outcome = %q (%s), want ok", res.Outcome, res.Reason)
	}
	if res.EnergyFullBeforeUWh != 112_700_000 || res.EnergyFullAfterUWh != 94_000_000 {
		t.Errorf("FCC before/after = %d/%d, want 112.7M/94M", res.EnergyFullBeforeUWh, res.EnergyFullAfterUWh)
	}
	assertResting(t, sim, 75, 80)
	if r, _ := sim.Read(); r.Capacity > 80 {
		t.Errorf("pack left at %d%%, should be drained to the band", r.Capacity)
	}
}

func TestAbortNoAC(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{Capacity: 79, Start: 20, Stop: 25, HasThresholds: true, ACOnline: false})
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	res, err := New(sim, clk, nil, stdParams()).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeAborted || !strings.Contains(res.Reason, "AC") {
		t.Errorf("outcome = %q (%s), want aborted/AC", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80) // restore still ran
}

func TestAbortForceDischargeUnsupported(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{
		Capacity: 79, Start: 75, Stop: 80, HasThresholds: true, ACOnline: true,
		Supported: []battery.Behaviour{battery.BehaviourAuto},
	})
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	res, err := New(sim, clk, nil, stdParams()).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeAborted || !strings.Contains(res.Reason, "force-discharge") {
		t.Errorf("outcome = %q (%s), want aborted/force-discharge", res.Outcome, res.Reason)
	}
}

func TestAbortACLostDuringDischarge(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	clk.after = func() {
		r, _ := sim.Read()
		b, _ := sim.Behaviour()
		if b == battery.BehaviourForceDischarge && r.Capacity < 90 {
			sim.SetAC(false)
		}
	}
	res, err := New(sim, clk, nil, stdParams()).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeAborted || !strings.Contains(res.Reason, "AC lost") {
		t.Errorf("outcome = %q (%s), want aborted/AC lost", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80)
}

func TestCancelRestores(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := false
	clk.after = func() {
		r, _ := sim.Read()
		b, _ := sim.Behaviour()
		if !cancelled && b == battery.BehaviourForceDischarge && r.Capacity <= 50 {
			cancelled = true
			cancel()
		}
	}
	res, err := New(sim, clk, nil, stdParams()).Run(ctx)
	cancel()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeAborted || !strings.Contains(res.Reason, "cancel") {
		t.Errorf("outcome = %q (%s), want aborted/cancelled", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80)
}

func TestChargeTimeout(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	p := stdParams()
	p.ChargeTimeout = time.Minute // far too short to reach full
	res, err := New(sim, clk, nil, p).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeFailed || !strings.Contains(res.Reason, "timed out") {
		t.Errorf("outcome = %q (%s), want failed/timed out", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80)
}

func assertResting(t *testing.T, sim *battery.Sim, start, stop int) {
	t.Helper()
	if b, _ := sim.Behaviour(); b != battery.BehaviourAuto {
		t.Errorf("behaviour left at %q, want auto", b)
	}
	st, sp, err := sim.Thresholds()
	if err != nil {
		t.Fatalf("thresholds: %v", err)
	}
	if st != start || sp != stop {
		t.Errorf("band left at %d/%d, want %d/%d", st, sp, start, stop)
	}
}
