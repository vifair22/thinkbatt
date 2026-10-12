package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/calib"
	"git.airies.net/vifair22/thinkbatt/internal/caps"
	"git.airies.net/vifair22/thinkbatt/internal/config"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// fakeClock advances virtual time and the simulated battery on each Sleep.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
	sim *battery.Sim
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	c.sim.Advance(d)
	return nil
}

type countInhibitor struct {
	mu    sync.Mutex
	count int
}

func (c *countInhibitor) Inhibit(string) (func(), error) {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	return func() {}, nil
}

func fullSetSim(cap int) *battery.Sim {
	return battery.NewSim(battery.SimConfig{
		Capacity: cap, Start: 75, Stop: 80, HasThresholds: true, ACOnline: true,
		EnergyFullUWh: 112_700_000, EnergyFullDesignUWh: 94_000_000, RelearnFloor: 10,
	})
}

func newDaemon(t *testing.T, bat battery.Battery, clk calib.Clock, inh Inhibitor) *Daemon {
	t.Helper()
	profile, err := caps.Detect(bat)
	if err != nil {
		t.Fatal(err)
	}
	d, err := New(Options{
		Battery:   bat,
		Profile:   profile,
		Config:    config.Default(),
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Clock:     clk,
		Inhibitor: inh,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// waitIdle polls until no calibration is running, failing on timeout.
func waitIdle(t *testing.T, d *Daemon) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := d.Status()
		if err != nil {
			t.Fatal(err)
		}
		if !info.CalibrationRunning {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("calibration did not finish in time")
}

func TestCalibrateNowRunsAndRecords(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	inh := &countInhibitor{}
	d := newDaemon(t, sim, clk, inh)

	if err := d.Calibrate(true); err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	waitIdle(t, d)

	info, _ := d.Status()
	if info.LastCalibration == nil || info.LastCalibration.Outcome != state.OutcomeOK {
		t.Fatalf("expected a recorded OK calibration, got %+v", info.LastCalibration)
	}
	if info.LastCalibration.EnergyFullAfterUWh != 94_000_000 {
		t.Errorf("FCC after = %d, want relearned 94M", info.LastCalibration.EnergyFullAfterUWh)
	}
	if inh.count != 1 {
		t.Errorf("inhibitor used %d times, want 1", inh.count)
	}
	// The run persisted state to disk.
	loaded, _ := state.Load(d.statePath)
	if loaded.LastCalibration == nil {
		t.Errorf("state not persisted")
	}
}

func TestCalibrateRejectedWhenCannot(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{
		Capacity: 79, Start: 75, Stop: 80, HasThresholds: true, ACOnline: true,
		Supported: []battery.Behaviour{battery.BehaviourAuto},
	})
	d := newDaemon(t, sim, &fakeClock{sim: sim}, &countInhibitor{})
	if err := d.Calibrate(true); err == nil {
		t.Errorf("Calibrate should be rejected on a machine that cannot force-discharge")
	}
}

func TestCalibrateNotDue(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, sim, clk, &countInhibitor{})

	// First run (forced) establishes a recent calibration.
	if err := d.Calibrate(true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, d)
	// Now a non-forced request is not due.
	if err := d.Calibrate(false); err == nil {
		t.Errorf("expected 'not due' when just calibrated")
	}
}

func TestTickRunsWhenDue(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, sim, clk, &countInhibitor{})

	d.Tick(clk.Now()) // no prior calibration -> due
	waitIdle(t, d)
	info, _ := d.Status()
	if info.LastCalibration == nil {
		t.Errorf("Tick did not start a due calibration")
	}
}

func TestCancelNothingRunning(t *testing.T) {
	sim := fullSetSim(79)
	d := newDaemon(t, sim, &fakeClock{sim: sim}, &countInhibitor{})
	if err := d.Cancel(); err == nil {
		t.Errorf("Cancel with nothing running should error")
	}
}

func TestSetBand(t *testing.T) {
	sim := fullSetSim(79)
	d := newDaemon(t, sim, &fakeClock{sim: sim}, &countInhibitor{})

	if err := d.SetBand(70, 85); err != nil {
		t.Fatalf("SetBand: %v", err)
	}
	start, stop, _ := sim.Thresholds()
	if start != 70 || stop != 85 {
		t.Errorf("band not re-asserted: %d/%d", start, stop)
	}
	if err := d.SetBand(90, 80); err == nil {
		t.Errorf("invalid band should error")
	}
}

func TestAssertBandStartup(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{Capacity: 50, Start: 10, Stop: 20, HasThresholds: true, ACOnline: true})
	d := newDaemon(t, sim, &fakeClock{sim: sim}, &countInhibitor{})
	if err := d.AssertBand(); err != nil {
		t.Fatalf("AssertBand: %v", err)
	}
	start, stop, _ := sim.Thresholds()
	if start != 75 || stop != 80 {
		t.Errorf("startup band = %d/%d, want defaults 75/80", start, stop)
	}
}

type fakeServer struct {
	done     chan struct{}
	served   atomic.Bool
	closedOK atomic.Bool
}

func (f *fakeServer) Serve() error { f.served.Store(true); <-f.done; return nil }
func (f *fakeServer) Close() error { close(f.done); f.closedOK.Store(true); return nil }

func TestServeLoopRunsTickAndShutsDown(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, sim, clk, &countInhibitor{})
	fs := &fakeServer{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() { errc <- d.Serve(ctx, fs, nil, clk.Now) }()

	// The startup Tick is due (no prior calibration) and runs one.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, _ := d.Status(); info.LastCalibration != nil && !info.CalibrationRunning {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	if err := <-errc; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if !fs.served.Load() || !fs.closedOK.Load() {
		t.Errorf("server not served/closed: served=%t closed=%t", fs.served.Load(), fs.closedOK.Load())
	}
	if info, _ := d.Status(); info.LastCalibration == nil {
		t.Errorf("startup tick did not run a calibration")
	}
}

// errBat makes Read fail, to exercise Status's error path.
type errBat struct{ *battery.Sim }

func (errBat) Read() (battery.Reading, error) {
	return battery.Reading{}, errReadFail
}

var errReadFail = &readErr{}

type readErr struct{}

func (*readErr) Error() string { return "read failed" }

func TestStatusReadError(t *testing.T) {
	sim := fullSetSim(79)
	d := newDaemon(t, errBat{sim}, &fakeClock{sim: sim}, &countInhibitor{})
	if _, err := d.Status(); err == nil {
		t.Errorf("Status should surface a battery read error")
	}
}

func TestSetBandWhileRunning(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, sim, clk, &countInhibitor{})
	// Force running=true without a real goroutine by starting then not waiting
	// is racy; instead drive the state directly under the lock.
	d.mu.Lock()
	d.running = true
	d.mu.Unlock()
	if err := d.SetBand(70, 85); err != nil {
		t.Errorf("SetBand while running should update config and return nil: %v", err)
	}
	d.mu.Lock()
	gotStart, gotStop := d.cfg.BandStart, d.cfg.BandStop
	d.mu.Unlock()
	if gotStart != 70 || gotStop != 85 {
		t.Errorf("config band not updated while running: %d/%d", gotStart, gotStop)
	}
	d.mu.Lock()
	d.running = false
	d.mu.Unlock()
}

func TestShutdownWaitsForRun(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, sim, clk, &countInhibitor{})
	if err := d.Calibrate(true); err != nil {
		t.Fatal(err)
	}
	d.Shutdown() // cancels and waits; must return
	info, _ := d.Status()
	if info.CalibrationRunning {
		t.Errorf("calibration still running after Shutdown")
	}
}
