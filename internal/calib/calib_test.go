package calib

import (
	"context"
	"errors"
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
	e := New(sim, clk, nil, stdParams(), nil)

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
	res, err := New(sim, clk, nil, stdParams(), nil).Run(context.Background())
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
	res, err := New(sim, clk, nil, stdParams(), nil).Run(context.Background())
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
	res, err := New(sim, clk, nil, stdParams(), nil).Run(context.Background())
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
	res, err := New(sim, clk, nil, stdParams(), nil).Run(ctx)
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
	res, err := New(sim, clk, nil, p, nil).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeFailed || !strings.Contains(res.Reason, "timed out") {
		t.Errorf("outcome = %q (%s), want failed/timed out", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80)
}

func TestChargeComplete(t *testing.T) {
	tests := []struct {
		name string
		r    battery.Reading
		want bool
	}{
		{"full at 100", battery.Reading{Status: battery.StatusFull, Capacity: 100}, true},
		{"not charging near full (gauge rounding)", battery.Reading{Status: battery.StatusNotCharging, Capacity: 98}, true},
		{"not charging exactly at the mark", battery.Reading{Status: battery.StatusNotCharging, Capacity: fullMark}, true},
		{"still charging at 100", battery.Reading{Status: battery.StatusCharging, Capacity: 100}, false},
		{"not charging but below the mark", battery.Reading{Status: battery.StatusNotCharging, Capacity: fullMark - 1}, false},
		{"brief pause mid-charge", battery.Reading{Status: battery.StatusNotCharging, Capacity: 60}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := chargeComplete(tc.r); got != tc.want {
				t.Errorf("chargeComplete(%+v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

// fakeAssist records how the engine drove the discharge load. Run drives the
// engine synchronously under the fake clock, so no locking is needed.
type fakeAssist struct {
	started    int
	stopped    int
	lastTarget float64
	startErr   error
}

func (a *fakeAssist) Start(_ context.Context, target float64) error {
	a.started++
	a.lastTarget = target
	return a.startErr
}

func (a *fakeAssist) Stop() { a.stopped++ }

func restParams() Params {
	p := stdParams()
	p.RestAtFull = time.Hour
	p.RestAtFloor = time.Hour
	p.DischargeCRate = 0.2 // C/5; on a 94 Wh design pack that is 18.8 W
	return p
}

func TestFullCycleWithRestsAndAssist(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	seen := map[battery.Behaviour]bool{}
	clk.after = func() {
		b, _ := sim.Behaviour()
		seen[b] = true
	}
	fa := &fakeAssist{}
	res, err := New(sim, clk, nil, restParams(), fa).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeOK {
		t.Fatalf("outcome = %q (%s), want ok", res.Outcome, res.Reason)
	}
	if res.EnergyFullAfterUWh != 94_000_000 {
		t.Errorf("FCC after = %d, want relearned 94M", res.EnergyFullAfterUWh)
	}
	if !seen[battery.BehaviourInhibitCharge] {
		t.Error("expected an inhibit-charge rest during the cycle")
	}
	if fa.started == 0 || fa.stopped < fa.started {
		t.Errorf("assist started %d / stopped %d, want started >=1 and stopped >= started", fa.started, fa.stopped)
	}
	if fa.lastTarget < 18.0 || fa.lastTarget > 19.6 {
		t.Errorf("assist target = %.2f W, want ~18.8 (C/5 of 94 Wh)", fa.lastTarget)
	}
	assertResting(t, sim, 75, 80)
}

func TestDischargeAssistStartErrorContinues(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	fa := &fakeAssist{startErr: errAbort{"no load available"}}
	res, err := New(sim, clk, nil, restParams(), fa).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	// A failed assist start is logged and the discharge proceeds at idle draw,
	// so the cycle still completes.
	if res.Outcome != state.OutcomeOK {
		t.Fatalf("outcome = %q (%s), want ok", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80)
}

func TestAbortACLostDuringRest(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	clk.after = func() {
		if b, _ := sim.Behaviour(); b == battery.BehaviourInhibitCharge {
			sim.SetAC(false)
		}
	}
	res, err := New(sim, clk, nil, restParams(), &fakeAssist{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeAborted || !strings.Contains(res.Reason, "AC lost during full rest") {
		t.Errorf("outcome = %q (%s), want aborted/AC lost during full rest", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80)
}

func TestCancelDuringRest(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := false
	clk.after = func() {
		if b, _ := sim.Behaviour(); b == battery.BehaviourInhibitCharge && !cancelled {
			cancelled = true
			cancel()
		}
	}
	res, err := New(sim, clk, nil, restParams(), &fakeAssist{}).Run(ctx)
	cancel()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeAborted || !strings.Contains(res.Reason, "cancel") {
		t.Errorf("outcome = %q (%s), want aborted/cancelled", res.Outcome, res.Reason)
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

// TestPhaseReported checks the engine names each phase while it is in it and
// clears the phase once the run is over.
func TestPhaseReported(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	e := New(sim, clk, nil, restParams(), nil)
	seen := map[string]bool{}
	clk.after = func() { seen[e.Phase()] = true }
	if _, err := e.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, p := range []string{PhaseCharging, PhaseRestFull, PhaseDischarging, PhaseRestFloor, PhaseRecharging, PhaseDrainingBand} {
		if !seen[p] {
			t.Errorf("phase %q never reported; saw %v", p, seen)
		}
	}
	if got := e.Phase(); got != "" {
		t.Errorf("phase after run = %q, want empty", got)
	}
}

// TestCancelCauseRecorded checks a cancellation with a cause records that cause
// as the abort reason.
func TestCancelCauseRecorded(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	ctx, cancel := context.WithCancelCause(context.Background())
	clk.after = func() { cancel(errors.New("cancelled for a test")) }
	res, err := New(sim, clk, nil, stdParams(), nil).Run(ctx)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeAborted || res.Reason != "cancelled for a test" {
		t.Errorf("outcome = %q (%s), want aborted with the cause", res.Outcome, res.Reason)
	}
}
