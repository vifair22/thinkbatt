// Package load holds a closed-loop controlled CPU load that holds a battery
// discharge at a target power. A calibration learning cycle has to discharge the
// pack at a chosen C-rate so it finishes in a bounded time; idle force-discharge
// is too slow (~8.5 W on the T430, ~12 h to empty), and the only continuously
// placeable power lever on a laptop is CPU work. A pool of duty-cycled busy
// goroutines is the actuator, the pack's power_now is the feedback, and a PI
// loop trims the duty. The duty-to-power map is nonlinear (DVFS), so the loop is
// deliberately model-free: it only needs the sign and rough scale of the error.
//
// This package carries no dependency on the calibration engine; the engine's
// DischargeAssist contract (Start/Stop) is satisfied structurally by Assist, and
// the feedback arrives through an injected measure func, so there is no import
// cycle and no battery dependency here.
package load

import (
	"context"
	"log/slog"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// Clock is the small time surface the control loop uses, redeclared here (rather
// than imported from calib) to keep this package cycle-free. A real clock and a
// virtual test clock both satisfy it.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the production Clock.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits for d or until ctx is done, returning ctx.Err() if cancelled.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Controller is a proportional-integral loop that drives a duty actuator to hold
// a measured value at a target. Gains are in duty per watt (Kp) and duty per
// watt-second (Ki).
type Controller struct {
	Kp, Ki   float64
	MinDuty  float64
	MaxDuty  float64
	Interval time.Duration
}

// Run trims duty each tick to hold measure() at target until ctx is done. A
// measure error is logged and skipped (the pack sensor stalls occasionally), not
// fatal. It returns ctx.Err() when the context ends.
func (c Controller) Run(ctx context.Context, target float64, measure func() (float64, error), setDuty func(float64), clock Clock, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	interval := c.Interval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	maxDuty := c.MaxDuty
	if maxDuty <= 0 {
		maxDuty = 1
	}
	var integral float64
	for {
		if v, err := measure(); err != nil {
			log.Warn("discharge load: measure failed", "err", err)
		} else {
			e := target - v
			step := e * interval.Seconds()
			integral += step
			duty := c.Kp*e + c.Ki*integral
			// Conditional integration: if the output saturates, undo this step's
			// integral so it cannot wind up while the actuator is pinned.
			switch {
			case duty > maxDuty:
				duty, integral = maxDuty, integral-step
			case duty < c.MinDuty:
				duty, integral = c.MinDuty, integral-step
			}
			setDuty(duty)
		}
		if err := clock.Sleep(ctx, interval); err != nil {
			return err
		}
	}
}

// CPULoad is the duty-cycled busy-work actuator: each of its workers spins for
// duty of a fixed period and sleeps the rest. The shared duty is stored as
// float64 bits in an atomic so SetDuty is lock-free from the control loop.
type CPULoad struct {
	workers int
	period  time.Duration
	duty    atomic.Uint64 // math.Float64bits of the duty in [0,1]
	iters   atomic.Uint64 // busy-spin iterations, exposed for tests

	mu      sync.Mutex
	stop    chan struct{}
	wg      sync.WaitGroup
	started bool
}

// NewCPULoad builds an actuator with the given worker count (at least 1).
func NewCPULoad(workers int) *CPULoad {
	if workers < 1 {
		workers = 1
	}
	return &CPULoad{workers: workers, period: 100 * time.Millisecond}
}

// SetDuty sets the busy fraction, clamped to [0,1].
func (l *CPULoad) SetDuty(d float64) {
	switch {
	case d < 0:
		d = 0
	case d > 1:
		d = 1
	}
	l.duty.Store(math.Float64bits(d))
}

// Duty returns the current duty.
func (l *CPULoad) Duty() float64 { return math.Float64frombits(l.duty.Load()) }

// Start launches the workers. It is a no-op if already started.
func (l *CPULoad) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started {
		return
	}
	l.started = true
	l.stop = make(chan struct{})
	for i := 0; i < l.workers; i++ {
		l.wg.Add(1)
		go l.worker(l.stop)
	}
}

func (l *CPULoad) worker(stop <-chan struct{}) {
	defer l.wg.Done()
	for {
		select {
		case <-stop:
			return
		default:
		}
		duty := l.Duty()
		onFor := time.Duration(float64(l.period) * duty)
		if onFor > 0 {
			deadline := time.Now().Add(onFor)
			for time.Now().Before(deadline) {
				l.iters.Add(1)
			}
		}
		if offFor := l.period - onFor; offFor > 0 {
			select {
			case <-stop:
				return
			case <-time.After(offFor):
			}
		}
	}
}

// Stop signals the workers and waits for them, then zeroes the duty. It is a
// no-op if not started.
func (l *CPULoad) Stop() {
	l.mu.Lock()
	if !l.started {
		l.mu.Unlock()
		return
	}
	l.started = false
	close(l.stop)
	l.mu.Unlock()
	l.wg.Wait()
	l.SetDuty(0)
}

// Assist wires a CPULoad and a Controller behind the calibration engine's
// DischargeAssist contract (Start/Stop). Feedback arrives through measure, which
// returns the pack's current power in watts.
type Assist struct {
	ctrl    Controller
	load    *CPULoad
	measure func() (float64, error)
	clock   Clock
	log     *slog.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// NewAssist builds an Assist. workers defaults to NumCPU when < 1; a nil clock
// uses the real clock. The PI gains are starting values tuned for a laptop
// discharge and are safe to adjust.
func NewAssist(workers int, measure func() (float64, error), clock Clock, log *slog.Logger) *Assist {
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Assist{
		// Ki trimmed from 0.0008 after the first T430 run showed a ~70 s limit
		// cycle (+/-2 W around target): the integral term was over-correcting
		// against the loop's deadtime. Halved for damping; still enough integral
		// action to zero the steady-state bias. Revisit with a second machine's
		// data before trimming further.
		ctrl:    Controller{Kp: 0.02, Ki: 0.0004, MinDuty: 0, MaxDuty: 1, Interval: 10 * time.Second},
		load:    NewCPULoad(workers),
		measure: measure,
		clock:   clock,
		log:     log,
	}
}

// Start begins holding the discharge at targetWatts. It is a no-op if already
// running.
func (a *Assist) Start(ctx context.Context, targetWatts float64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cancel != nil {
		return nil
	}
	cctx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.done = make(chan struct{})
	a.load.Start()
	go func() {
		defer close(a.done)
		_ = a.ctrl.Run(cctx, targetWatts, a.measure, a.load.SetDuty, a.clock, a.log)
	}()
	return nil
}

// Stop ends the control loop and tears down the load. It blocks until both are
// done and is safe to call when not running.
func (a *Assist) Stop() {
	a.mu.Lock()
	cancel, done := a.cancel, a.done
	a.cancel, a.done = nil, nil
	a.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	a.load.Stop()
}
