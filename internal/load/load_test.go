package load

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

var errSensor = errors.New("sensor stalled")

// fakeClock runs the control loop in virtual time: Sleep returns immediately,
// counts ticks, and cancels the context after max ticks so Run terminates.
type fakeClock struct {
	now    time.Time
	ticks  int
	max    int
	cancel context.CancelFunc
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = c.now.Add(d)
	c.ticks++
	if c.max > 0 && c.ticks >= c.max && c.cancel != nil {
		c.cancel()
	}
	return nil
}

// plant maps duty to power linearly: power = base + gain*duty. Single-goroutine
// use (the controller runs synchronously under the fake clock).
type plant struct {
	base, gain float64
	duty       float64
}

func (p *plant) set(d float64)             { p.duty = d }
func (p *plant) measure() (float64, error) { return p.base + p.gain*p.duty, nil }
func (p *plant) power() float64            { return p.base + p.gain*p.duty }

func stdController() Controller {
	return Controller{Kp: 0.02, Ki: 0.0008, MinDuty: 0, MaxDuty: 1, Interval: 10 * time.Second}
}

func runController(t *testing.T, c Controller, target float64, measure func() (float64, error), set func(float64), maxTicks int) {
	t.Helper()
	clk := &fakeClock{max: maxTicks}
	ctx, cancel := context.WithCancel(context.Background())
	clk.cancel = cancel
	defer cancel()
	if err := c.Run(ctx, target, measure, set, clk, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v, want context.Canceled", err)
	}
}

func TestControllerConverges(t *testing.T) {
	p := &plant{base: 8, gain: 24} // reachable range [8, 32] W
	runController(t, stdController(), 18.8, p.measure, p.set, 400)
	if got := p.power(); math.Abs(got-18.8) > 0.5 {
		t.Errorf("held %.2f W, want ~18.8 (duty %.3f)", got, p.duty)
	}
}

func TestControllerSaturatesHigh(t *testing.T) {
	p := &plant{base: 8, gain: 24} // max 32 W, target above it
	runController(t, stdController(), 40, p.measure, p.set, 500)
	if p.duty != 1 {
		t.Errorf("duty = %.3f, want pinned at 1 for an unreachable target", p.duty)
	}
}

func TestControllerSaturatesLow(t *testing.T) {
	p := &plant{base: 8, gain: 24} // min 8 W, target below it
	runController(t, stdController(), 2, p.measure, p.set, 100)
	if p.duty != 0 {
		t.Errorf("duty = %.3f, want pinned at 0 for a sub-idle target", p.duty)
	}
}

func TestControllerMeasureErrorTolerated(t *testing.T) {
	p := &plant{base: 8, gain: 24}
	calls := 0
	measure := func() (float64, error) {
		calls++
		if calls < 6 {
			return 0, errSensor
		}
		return p.measure()
	}
	runController(t, stdController(), 18.8, measure, p.set, 400)
	if got := p.power(); math.Abs(got-18.8) > 0.5 {
		t.Errorf("held %.2f W after sensor recovery, want ~18.8", got)
	}
}

func TestControllerCtxCancel(t *testing.T) {
	p := &plant{base: 8, gain: 24}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := stdController().Run(ctx, 18.8, p.measure, p.set, &fakeClock{}, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("Run with cancelled ctx returned %v, want context.Canceled", err)
	}
}

func TestCPULoadClamp(t *testing.T) {
	l := NewCPULoad(2)
	l.SetDuty(2)
	if l.Duty() != 1 {
		t.Errorf("SetDuty(2) -> %.2f, want clamp to 1", l.Duty())
	}
	l.SetDuty(-1)
	if l.Duty() != 0 {
		t.Errorf("SetDuty(-1) -> %.2f, want clamp to 0", l.Duty())
	}
}

func TestCPULoadRunsAndStops(t *testing.T) {
	l := NewCPULoad(2)
	l.SetDuty(1)
	l.Start()
	time.Sleep(40 * time.Millisecond)
	before := l.iters.Load()
	time.Sleep(60 * time.Millisecond)
	after := l.iters.Load()
	l.Stop()
	if after <= before {
		t.Errorf("busy-work iterations did not advance at duty 1 (%d -> %d)", before, after)
	}
	if l.Duty() != 0 {
		t.Errorf("duty after Stop = %.2f, want 0", l.Duty())
	}
	l.Stop() // idempotent, must not hang or panic
}

func TestCPULoadIdleBarelySpins(t *testing.T) {
	l := NewCPULoad(1)
	l.SetDuty(0)
	l.Start()
	defer l.Stop()
	time.Sleep(40 * time.Millisecond)
	before := l.iters.Load()
	time.Sleep(60 * time.Millisecond)
	if after := l.iters.Load(); after != before {
		t.Errorf("duty 0 should do no busy work, iters moved %d -> %d", before, after)
	}
}

func TestAssistHoldsTarget(t *testing.T) {
	// Close the loop through the real actuator: the plant reads the actuator's
	// duty, so Start drives the actuator to hold the target, then Stop tears down.
	var load *CPULoad
	const base, gain = 8.0, 24.0
	measure := func() (float64, error) { return base + gain*load.Duty(), nil }

	clk := &fakeClock{max: 400}
	ctx, cancel := context.WithCancel(context.Background())
	clk.cancel = cancel
	defer cancel()

	a := NewAssist(1, measure, clk, nil)
	load = a.load
	if err := a.Start(ctx, 18.8); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(ctx, 18.8); err != nil { // second Start is a no-op
		t.Fatal(err)
	}
	<-ctx.Done()
	got := base + gain*a.load.Duty()
	a.Stop()
	a.Stop() // idempotent
	if math.Abs(got-18.8) > 1.0 {
		t.Errorf("assist held %.2f W, want ~18.8", got)
	}
}
