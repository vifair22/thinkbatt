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

// failBat wraps a Sim and can be made to fail Supported, to exercise the
// engine's error path.
type failBat struct {
	*battery.Sim
	failSupported    bool
	failSetBehaviour bool
}

func (f *failBat) Supported() ([]battery.Behaviour, error) {
	if f.failSupported {
		return nil, errors.New("sysfs read failed")
	}
	return f.Sim.Supported()
}

func (f *failBat) SetBehaviour(b battery.Behaviour) error {
	if f.failSetBehaviour {
		return errors.New("sysfs write failed")
	}
	return f.Sim.SetBehaviour(b)
}

func TestRunSupportedError(t *testing.T) {
	sim := fullSetSim(79)
	fb := &failBat{Sim: sim, failSupported: true}
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	_, err := New(fb, clk, nil, stdParams()).Run(context.Background())
	if err == nil {
		t.Errorf("expected error when Supported fails")
	}
}

func TestRunSetBehaviourError(t *testing.T) {
	// A write failure during the cycle fails the run, and the restore (which
	// also writes) is attempted on the way out.
	sim := fullSetSim(79)
	fb := &failBat{Sim: sim, failSetBehaviour: true}
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	res, err := New(fb, clk, nil, stdParams()).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeFailed {
		t.Errorf("outcome = %q, want failed", res.Outcome)
	}
}

func TestDischargeTimeout(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	p := stdParams()
	p.DischargeTimeout = time.Minute // reaches full, then cannot drain in time
	res, err := New(sim, clk, nil, p).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeFailed || !strings.Contains(res.Reason, "timed out") {
		t.Errorf("outcome = %q (%s), want failed/timed out", res.Outcome, res.Reason)
	}
	assertResting(t, sim, 75, 80)
}

func TestDrainNoopWhenStopIsFull(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	p := stdParams()
	p.BandStop = 100 // after the recharge to full there is nothing to drain
	res, err := New(sim, clk, nil, p).Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Outcome != state.OutcomeOK {
		t.Errorf("outcome = %q (%s), want ok", res.Outcome, res.Reason)
	}
}
