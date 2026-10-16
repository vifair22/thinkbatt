package daemon

import (
	"context"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/calib"
	"git.airies.net/vifair22/thinkbatt/internal/config"
	"git.airies.net/vifair22/thinkbatt/internal/control"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// fakeClock advances virtual time and one simulated pack on each Sleep. Only
// one pack calibrates at a time, so a single sim is all a run needs to move.
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

// fakeSet is an in-memory battery.Set whose membership can change at runtime to
// exercise hot-plug discovery.
type fakeSet struct {
	mu   sync.Mutex
	bats map[string]battery.Battery
}

func newFakeSet(bats map[string]battery.Battery) *fakeSet {
	cp := make(map[string]battery.Battery, len(bats))
	for k, v := range bats {
		cp[k] = v
	}
	return &fakeSet{bats: cp}
}

func (f *fakeSet) List() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.bats))
	for n := range f.bats {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

func (f *fakeSet) Open(name string) battery.Battery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bats[name]
}

func (f *fakeSet) set(name string, b battery.Battery) {
	f.mu.Lock()
	f.bats[name] = b
	f.mu.Unlock()
}

func (f *fakeSet) remove(name string) {
	f.mu.Lock()
	delete(f.bats, name)
	f.mu.Unlock()
}

func fullSetSim(cap int) *battery.Sim {
	return battery.NewSim(battery.SimConfig{
		Capacity: cap, Start: 75, Stop: 80, HasThresholds: true, ACOnline: true,
		EnergyFullUWh: 112_700_000, EnergyFullDesignUWh: 94_000_000, RelearnFloor: 10,
	})
}

func singlePack(b battery.Battery) *fakeSet {
	return newFakeSet(map[string]battery.Battery{"BAT0": b})
}

func newDaemon(t *testing.T, set battery.Set, clk calib.Clock, inh Inhibitor) *Daemon {
	t.Helper()
	d, err := New(Options{
		Set:       set,
		File:      config.File{Global: config.Default()},
		StatePath: filepath.Join(t.TempDir(), "state.json"),
		Clock:     clk,
		Inhibitor: inh,
	})
	if err != nil {
		t.Fatal(err)
	}
	d.Refresh() // populate the managed set before the test acts
	return d
}

func packCount(d *Daemon) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.packs)
}

func anyRunning(info control.StatusInfo) bool {
	for _, p := range info.Packs {
		if p.CalibrationRunning {
			return true
		}
	}
	return false
}

func lastOf(info control.StatusInfo, name string) *state.Calibration {
	for _, p := range info.Packs {
		if p.Name == name {
			return p.LastCalibration
		}
	}
	return nil
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
		if !anyRunning(info) {
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
	d := newDaemon(t, singlePack(sim), clk, inh)

	if err := d.Calibrate("", true); err != nil {
		t.Fatalf("Calibrate: %v", err)
	}
	waitIdle(t, d)

	info, _ := d.Status()
	lc := lastOf(info, "BAT0")
	if lc == nil || lc.Outcome != state.OutcomeOK {
		t.Fatalf("expected a recorded OK calibration, got %+v", lc)
	}
	if lc.EnergyFullAfterUWh != 94_000_000 {
		t.Errorf("FCC after = %d, want relearned 94M", lc.EnergyFullAfterUWh)
	}
	if inh.count != 1 {
		t.Errorf("inhibitor used %d times, want 1", inh.count)
	}
	// The run persisted state to disk.
	loaded, _ := state.Load(d.statePath)
	if loaded.Last("BAT0") == nil {
		t.Errorf("state not persisted")
	}
}

func TestCalibrateRejectedWhenCannot(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{
		Capacity: 79, Start: 75, Stop: 80, HasThresholds: true, ACOnline: true,
		Supported: []battery.Behaviour{battery.BehaviourAuto},
	})
	d := newDaemon(t, singlePack(sim), &fakeClock{sim: sim}, &countInhibitor{})
	if err := d.Calibrate("", true); err == nil {
		t.Errorf("Calibrate should be rejected on a machine that cannot force-discharge")
	}
}

func TestCalibrateNotDue(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})

	// First run (forced) establishes a recent calibration.
	if err := d.Calibrate("", true); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, d)
	// Now a non-forced request is not due.
	if err := d.Calibrate("", false); err == nil {
		t.Errorf("expected 'not due' when just calibrated")
	}
}

func TestMaintainRunsWhenDue(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})

	d.Maintain(clk.Now()) // no prior calibration -> due
	waitIdle(t, d)
	info, _ := d.Status()
	if lastOf(info, "BAT0") == nil {
		t.Errorf("Maintain did not start a due calibration")
	}
}

func TestCancelNothingRunning(t *testing.T) {
	sim := fullSetSim(79)
	d := newDaemon(t, singlePack(sim), &fakeClock{sim: sim}, &countInhibitor{})
	if err := d.Cancel(); err == nil {
		t.Errorf("Cancel with nothing running should error")
	}
}

func TestSetBand(t *testing.T) {
	sim := fullSetSim(79)
	d := newDaemon(t, singlePack(sim), &fakeClock{sim: sim}, &countInhibitor{})

	if err := d.SetBand("", 70, 85); err != nil {
		t.Fatalf("SetBand: %v", err)
	}
	start, stop, _ := sim.Thresholds()
	if start != 70 || stop != 85 {
		t.Errorf("band not re-asserted: %d/%d", start, stop)
	}
	if err := d.SetBand("", 90, 80); err == nil {
		t.Errorf("invalid band should error")
	}
}

func TestAssertBandStartup(t *testing.T) {
	sim := battery.NewSim(battery.SimConfig{Capacity: 50, Start: 10, Stop: 20, HasThresholds: true, ACOnline: true})
	d := newDaemon(t, singlePack(sim), &fakeClock{sim: sim}, &countInhibitor{})
	d.assertBands()
	start, stop, _ := sim.Thresholds()
	if start != 75 || stop != 80 {
		t.Errorf("startup band = %d/%d, want defaults 75/80", start, stop)
	}
}

func TestRefreshAddsAndRemovesPacks(t *testing.T) {
	sim0 := fullSetSim(79)
	set := singlePack(sim0)
	d := newDaemon(t, set, &fakeClock{sim: sim0}, &countInhibitor{})
	if got := packCount(d); got != 1 {
		t.Fatalf("want 1 pack after startup, got %d", got)
	}

	// Dock a second pack: discovery picks it up and bands it.
	sim1 := battery.NewSim(battery.SimConfig{Capacity: 50, Start: 10, Stop: 20, HasThresholds: true, ACOnline: true})
	set.set("BAT1", sim1)
	d.Maintain(time.Unix(0, 0))
	if got := packCount(d); got != 2 {
		t.Fatalf("want 2 packs after docking, got %d", got)
	}
	if start, stop, _ := sim1.Thresholds(); start != 75 || stop != 80 {
		t.Errorf("docked pack not banded: %d/%d", start, stop)
	}

	// Pull it: discovery drops it.
	set.remove("BAT1")
	d.Refresh()
	if got := packCount(d); got != 1 {
		t.Errorf("want 1 pack after removal, got %d", got)
	}
}

func TestCalibrateMultiPackTargeting(t *testing.T) {
	sim0 := fullSetSim(79)
	sim1 := fullSetSim(79)
	set := newFakeSet(map[string]battery.Battery{"BAT0": sim0, "BAT1": sim1})
	// The clock drives BAT1's sim, the pack we calibrate.
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim1}
	d := newDaemon(t, set, clk, &countInhibitor{})

	// Ambiguous with two packs present and no target named.
	if err := d.Calibrate("", true); err == nil {
		t.Errorf("Calibrate with two packs and no --battery should error")
	}
	// Unknown pack named.
	if err := d.Calibrate("BAT9", true); err == nil {
		t.Errorf("Calibrate of a nonexistent pack should error")
	}
	// Targeted at BAT1.
	if err := d.Calibrate("BAT1", true); err != nil {
		t.Fatalf("Calibrate BAT1: %v", err)
	}
	waitIdle(t, d)
	info, _ := d.Status()
	if lastOf(info, "BAT1") == nil {
		t.Errorf("BAT1 calibration not recorded")
	}
	if lastOf(info, "BAT0") != nil {
		t.Errorf("BAT0 should not have calibrated")
	}
}

type fakeServer struct {
	done     chan struct{}
	served   atomic.Bool
	closedOK atomic.Bool
}

func (f *fakeServer) Serve() error { f.served.Store(true); <-f.done; return nil }
func (f *fakeServer) Close() error { close(f.done); f.closedOK.Store(true); return nil }

func TestServeLoopRunsMaintainAndShutsDown(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})
	fs := &fakeServer{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())

	errc := make(chan error, 1)
	go func() { errc <- d.Serve(ctx, fs, nil, clk.Now) }()

	// The startup Maintain is due (no prior calibration) and runs one.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if info, _ := d.Status(); lastOf(info, "BAT0") != nil && !anyRunning(info) {
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
	if info, _ := d.Status(); lastOf(info, "BAT0") == nil {
		t.Errorf("startup Maintain did not run a calibration")
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
	d := newDaemon(t, singlePack(errBat{sim}), &fakeClock{sim: sim}, &countInhibitor{})
	if _, err := d.Status(); err == nil {
		t.Errorf("Status should surface a battery read error")
	}
}

func TestSetBandWhileRunning(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})
	// Drive the running flags directly under the lock rather than racing a real
	// goroutine; SetBand must still update the pack's config and not block.
	d.mu.Lock()
	d.running = true
	d.runningPack = "BAT0"
	d.mu.Unlock()
	if err := d.SetBand("", 70, 85); err != nil {
		t.Errorf("SetBand while running should update config and return nil: %v", err)
	}
	d.mu.Lock()
	gotStart, gotStop := d.packs["BAT0"].cfg.BandStart, d.packs["BAT0"].cfg.BandStop
	d.mu.Unlock()
	if gotStart != 70 || gotStop != 85 {
		t.Errorf("config band not updated while running: %d/%d", gotStart, gotStop)
	}
	d.mu.Lock()
	d.running = false
	d.runningPack = ""
	d.mu.Unlock()
}

func TestShutdownWaitsForRun(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})
	if err := d.Calibrate("", true); err != nil {
		t.Fatal(err)
	}
	d.Shutdown() // cancels and waits; must return
	info, _ := d.Status()
	if anyRunning(info) {
		t.Errorf("calibration still running after Shutdown")
	}
}

// blockClock parks a calibration in its first wait until it is cancelled, so a
// test can act on a run that is reliably in progress.
type blockClock struct{ now time.Time }

func (c *blockClock) Now() time.Time { return c.now }

func (c *blockClock) Sleep(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

func thresholds(t *testing.T, b battery.Battery) (int, int) {
	t.Helper()
	start, stop, err := b.Thresholds()
	if err != nil {
		t.Fatal(err)
	}
	return start, stop
}

// waitPhase polls until the pack reports a calibration phase.
func waitPhase(t *testing.T, d *Daemon, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		info, err := d.Status()
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range info.Packs {
			if p.CalibrationPhase == want {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("calibration never reached phase %q", want)
}

func TestOverrideSetPersistsAndReloads(t *testing.T) {
	sim0, sim1 := fullSetSim(79), fullSetSim(60)
	set := newFakeSet(map[string]battery.Battery{"BAT0": sim0, "BAT1": sim1})
	clk := &fakeClock{now: time.Unix(1_000_000, 0), sim: sim0}
	d := newDaemon(t, set, clk, &countInhibitor{})

	deadline := clk.Now().Add(8 * time.Hour)
	if err := d.SetOverride("", 100, deadline, false); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	for name, sim := range map[string]*battery.Sim{"BAT0": sim0, "BAT1": sim1} {
		if start, stop := thresholds(t, sim); start != 99 || stop != 100 {
			t.Errorf("%s thresholds = %d/%d, want 99/100", name, start, stop)
		}
	}
	info, _ := d.Status()
	for _, p := range info.Packs {
		if p.Override == nil || p.Override.Target != 100 || !p.Override.Until.Equal(deadline) {
			t.Errorf("%s status override = %+v", p.Name, p.Override)
		}
		if p.BandStart != 75 || p.BandStop != 80 {
			t.Errorf("%s resting band reported as %d/%d, want 75/80", p.Name, p.BandStart, p.BandStop)
		}
	}

	// A daemon restarted on the same state file comes back with the override.
	d2, err := New(Options{Set: set, File: config.File{Global: config.Default()}, StatePath: d.statePath, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	d2.Refresh()
	_ = sim0.SetThresholds(75, 80) // a firmware reset across the restart
	d2.Maintain(clk.Now())
	if start, stop := thresholds(t, sim0); start != 99 || stop != 100 {
		t.Errorf("override not re-applied after restart: %d/%d", start, stop)
	}
}

func TestOverrideBlocksCalibration(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})
	if err := d.SetOverride("", 100, time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if err := d.Calibrate("", true); err == nil {
		t.Errorf("Calibrate --now should be refused under an override")
	}
	d.Maintain(clk.Now()) // never calibrated, so due, but the override blocks it
	if info, _ := d.Status(); anyRunning(info) || lastOf(info, "BAT0") != nil {
		t.Errorf("Maintain started a calibration on an overridden pack")
	}
}

func TestOverrideDeadlineExpires(t *testing.T) {
	sim := fullSetSim(79)
	set := singlePack(sim)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, set, clk, &countInhibitor{})
	d.mu.Lock()
	d.packs["BAT0"].cfg.CalibEnabled = false // keep the due-check out of the way
	d.mu.Unlock()

	deadline := clk.Now().Add(time.Hour)
	if err := d.SetOverride("BAT0", 100, deadline, false); err != nil {
		t.Fatal(err)
	}
	d.Maintain(deadline.Add(-time.Minute))
	if start, stop := thresholds(t, sim); start != 99 || stop != 100 {
		t.Errorf("override ended early: %d/%d", start, stop)
	}
	d.Maintain(deadline)
	if start, stop := thresholds(t, sim); start != 75 || stop != 80 {
		t.Errorf("band not restored at deadline: %d/%d", start, stop)
	}
	if loaded, _ := state.Load(d.statePath); loaded.Override("BAT0") != nil {
		t.Errorf("expired override still persisted")
	}

	// A deadline override on a pack that has since been pulled still expires.
	if err := d.SetOverride("BAT0", 100, deadline.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	set.remove("BAT0")
	d.Maintain(deadline.Add(2 * time.Hour))
	d.mu.Lock()
	left := d.st.Override("BAT0")
	d.mu.Unlock()
	if left != nil {
		t.Errorf("deadline override on an absent pack did not expire")
	}
}

func TestOverrideUntilFullExpires(t *testing.T) {
	sim := fullSetSim(79)
	clk := &fakeClock{now: time.Unix(0, 0), sim: sim}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})
	d.mu.Lock()
	d.packs["BAT0"].cfg.CalibEnabled = false
	d.mu.Unlock()

	if err := d.SetOverride("", 100, time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	d.Maintain(clk.Now())
	if start, stop := thresholds(t, sim); start != 99 || stop != 100 {
		t.Fatalf("until-full override ended before charging: %d/%d", start, stop)
	}
	// Charge the sim to full in virtual time, then the next pass ends it.
	_ = clk.Sleep(context.Background(), 2*time.Hour)
	d.Maintain(clk.Now())
	if start, stop := thresholds(t, sim); start != 75 || stop != 80 {
		t.Errorf("band not restored once full: %d/%d", start, stop)
	}
	if r, _ := sim.Read(); r.Capacity < 97 {
		t.Errorf("sim only reached %d%%", r.Capacity)
	}
}

func TestOverrideCancelsRunningCalibration(t *testing.T) {
	sim := fullSetSim(79)
	clk := &blockClock{now: time.Unix(0, 0)}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})

	if err := d.Calibrate("", true); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, d, calib.PhaseCharging)
	if err := d.SetOverride("", 90, clk.Now().Add(time.Hour), false); err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	// SetOverride waited the run out: it is over, recorded, and the override's
	// band is what the pack is left at, not the engine's resting-band restore.
	info, _ := d.Status()
	if anyRunning(info) {
		t.Errorf("calibration still running after the override")
	}
	if lc := lastOf(info, "BAT0"); lc == nil || lc.Outcome != state.OutcomeAborted || lc.Reason != errCancelOverride.Error() {
		t.Errorf("last calibration = %+v, want aborted for the override", lc)
	}
	if start, stop := thresholds(t, sim); start != 89 || stop != 90 {
		t.Errorf("thresholds = %d/%d, want the override's 89/90", start, stop)
	}
}

func TestBandChangedDuringRunAppliesAtEnd(t *testing.T) {
	sim := fullSetSim(79)
	clk := &blockClock{now: time.Unix(0, 0)}
	d := newDaemon(t, singlePack(sim), clk, &countInhibitor{})
	if err := d.Calibrate("", true); err != nil {
		t.Fatal(err)
	}
	waitPhase(t, d, calib.PhaseCharging)
	if err := d.SetBand("", 60, 70); err != nil {
		t.Fatal(err)
	}
	if err := d.Cancel(); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, d)
	if start, stop := thresholds(t, sim); start != 60 || stop != 70 {
		t.Errorf("thresholds after run = %d/%d, want the band set mid-run 60/70", start, stop)
	}
	info, _ := d.Status()
	if lc := lastOf(info, "BAT0"); lc == nil || lc.Reason != errCancelUser.Error() {
		t.Errorf("last calibration = %+v, want cancelled by user", lc)
	}
}

func TestSetBandUnderOverride(t *testing.T) {
	sim := fullSetSim(79)
	d := newDaemon(t, singlePack(sim), &fakeClock{now: time.Unix(0, 0), sim: sim}, &countInhibitor{})
	if err := d.SetOverride("", 100, time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetBand("", 50, 60); err != nil {
		t.Fatal(err)
	}
	if start, stop := thresholds(t, sim); start != 99 || stop != 100 {
		t.Errorf("SetBand displaced the override: %d/%d", start, stop)
	}
	if err := d.ClearOverride("BAT0"); err != nil {
		t.Fatal(err)
	}
	if start, stop := thresholds(t, sim); start != 50 || stop != 60 {
		t.Errorf("clearing the override did not return to the new band: %d/%d", start, stop)
	}
}

func TestOverrideErrors(t *testing.T) {
	sim := fullSetSim(79)
	noThresh := battery.NewSim(battery.SimConfig{Capacity: 50, ACOnline: true})
	set := newFakeSet(map[string]battery.Battery{"BAT0": sim, "BAT1": noThresh})
	clk := &fakeClock{now: time.Unix(1_000_000, 0), sim: sim}
	d := newDaemon(t, set, clk, &countInhibitor{})

	bad := []struct {
		name   string
		bat    string
		target int
		until  time.Time
		full   bool
	}{
		{"target too low", "", 1, time.Time{}, true},
		{"target too high", "", 101, time.Time{}, true},
		{"deadline in the past", "", 100, clk.Now().Add(-time.Minute), false},
		{"unknown pack", "BAT9", 100, time.Time{}, true},
		{"pack without thresholds", "BAT1", 100, time.Time{}, true},
	}
	for _, c := range bad {
		if err := d.SetOverride(c.bat, c.target, c.until, c.full); err == nil {
			t.Errorf("%s: SetOverride should error", c.name)
		}
	}
	if err := d.ClearOverride(""); err == nil {
		t.Errorf("ClearOverride with none active should error")
	}
	if err := d.SetOverride("BAT0", 100, time.Time{}, true); err != nil {
		t.Fatal(err)
	}
	if err := d.ClearOverride("BAT1"); err == nil {
		t.Errorf("ClearOverride on a pack without one should error")
	}
	if err := d.ClearOverride(""); err != nil {
		t.Errorf("ClearOverride of all: %v", err)
	}

	// No band-capable pack at all.
	only := newDaemon(t, newFakeSet(map[string]battery.Battery{"BAT1": noThresh}), clk, &countInhibitor{})
	if err := only.SetOverride("", 100, time.Time{}, true); err == nil {
		t.Errorf("SetOverride with no band-capable pack should error")
	}
}

func TestSignedPower(t *testing.T) {
	if got := signedPower(battery.Reading{Status: battery.StatusDischarging, PowerNowUW: 7}); got != -7 {
		t.Errorf("discharging power = %d, want -7", got)
	}
	if got := signedPower(battery.Reading{Status: battery.StatusCharging, PowerNowUW: 7}); got != 7 {
		t.Errorf("charging power = %d, want 7", got)
	}
}
