// Package daemon is thinkbatt's orchestration: it owns the battery, asserts the
// resting band, decides when calibration is due, runs at most one calibration
// at a time, and answers the control socket. It is split out from cmd/thinkbattd
// so the policy and concurrency can be tested against a simulated battery and a
// virtual clock. Flag parsing, signal handling, and sleep inhibition stay in
// the command.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/calib"
	"git.airies.net/vifair22/thinkbatt/internal/caps"
	"git.airies.net/vifair22/thinkbatt/internal/config"
	"git.airies.net/vifair22/thinkbatt/internal/control"
	"git.airies.net/vifair22/thinkbatt/internal/policy"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// Inhibitor blocks system sleep for the duration of a calibration so a lid
// close cannot suspend the machine mid-cycle. Inhibit returns a release func;
// a best-effort implementation may return a no-op release and a nil error.
type Inhibitor interface {
	Inhibit(reason string) (release func(), err error)
}

// nopInhibitor is used when no sleep inhibition is available.
type nopInhibitor struct{}

func (nopInhibitor) Inhibit(string) (func(), error) { return func() {}, nil }

// Options configures a Daemon.
type Options struct {
	Battery   battery.Battery
	Profile   caps.Profile
	Config    config.Config
	StatePath string
	Clock     calib.Clock // engine clock; nil uses the real clock
	Inhibitor Inhibitor   // nil uses a no-op
	Logger    *slog.Logger
}

// Daemon holds the live state behind the control socket.
type Daemon struct {
	bat       battery.Battery
	profile   caps.Profile
	statePath string
	clock     calib.Clock
	inhibitor Inhibitor
	log       *slog.Logger

	mu        sync.Mutex
	cfg       config.Config
	st        state.State
	running   bool
	cancel    context.CancelFunc
	runReason string
	wg        sync.WaitGroup
}

// New builds a Daemon and loads any persisted state.
func New(opts Options) (*Daemon, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Inhibitor == nil {
		opts.Inhibitor = nopInhibitor{}
	}
	if opts.Clock == nil {
		opts.Clock = calib.RealClock{}
	}
	st, err := state.Load(opts.StatePath)
	if err != nil {
		return nil, err
	}
	return &Daemon{
		bat:       opts.Battery,
		profile:   opts.Profile,
		statePath: opts.StatePath,
		clock:     opts.Clock,
		inhibitor: opts.Inhibitor,
		log:       opts.Logger,
		cfg:       opts.Config,
		st:        st,
	}, nil
}

// AssertBand puts the pack into the resting band and auto behaviour. Called at
// startup; the daemon is the band's owner.
func (d *Daemon) AssertBand() error {
	d.mu.Lock()
	start, stop := d.cfg.BandStart, d.cfg.BandStop
	d.mu.Unlock()
	return policy.AssertBand(d.bat, start, stop)
}

// Tick is the periodic due-check. If calibration is enabled, possible on this
// machine, not already running, and due, it starts one.
func (d *Daemon) Tick(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.cfg.CalibEnabled || !d.profile.CanCalibrate() || d.running {
		return
	}
	r, err := d.bat.Read()
	if err != nil {
		d.log.Warn("tick: battery read failed", "err", err)
		return
	}
	due, reason := policy.Due(d.st.LastCalibration, r, now, d.cfg.CalibEveryCycles, d.cfg.CalibEveryDays)
	if !due {
		return
	}
	d.startLocked(reason)
}

// Calibrate requests a run. now bypasses the due-check; otherwise the run only
// starts if currently due.
func (d *Daemon) Calibrate(now bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.profile.CanCalibrate() {
		return fmt.Errorf("this machine cannot calibrate: %s", d.profile.Summary())
	}
	if d.running {
		return fmt.Errorf("a calibration is already running")
	}
	if !now {
		r, err := d.bat.Read()
		if err != nil {
			return fmt.Errorf("battery read: %w", err)
		}
		due, reason := policy.Due(d.st.LastCalibration, r, d.clock.Now(), d.cfg.CalibEveryCycles, d.cfg.CalibEveryDays)
		if !due {
			return fmt.Errorf("not due: %s (use --now to force)", reason)
		}
		d.startLocked(reason)
		return nil
	}
	d.startLocked("manual")
	return nil
}

// startLocked launches the calibration goroutine. The caller holds d.mu.
func (d *Daemon) startLocked(reason string) {
	ctx, cancel := context.WithCancel(context.Background())
	d.running = true
	d.cancel = cancel
	d.runReason = reason
	d.wg.Add(1)
	d.log.Info("calibration starting", "reason", reason)
	params := calib.Params{
		BandStart: d.cfg.BandStart,
		BandStop:  d.cfg.BandStop,
		Floor:     d.cfg.CalibFloor,
	}
	go d.run(ctx, cancel, params)
}

func (d *Daemon) run(ctx context.Context, cancel context.CancelFunc, params calib.Params) {
	defer d.wg.Done()
	defer cancel()

	release, err := d.inhibitor.Inhibit("battery calibration")
	if err != nil {
		d.log.Warn("could not inhibit sleep; proceeding", "err", err)
		release = func() {}
	}
	defer release()

	started := d.clock.Now()
	engine := calib.New(d.bat, d.clock, d.log, params)
	res, runErr := engine.Run(ctx)

	rec := state.Calibration{
		StartedAt:           started,
		FinishedAt:          d.clock.Now(),
		Outcome:             res.Outcome,
		Reason:              res.Reason,
		CycleCountAt:        res.CycleCount,
		EnergyFullBeforeUWh: res.EnergyFullBeforeUWh,
		EnergyFullAfterUWh:  res.EnergyFullAfterUWh,
	}
	if runErr != nil {
		rec.Outcome = state.OutcomeFailed
		rec.Reason = runErr.Error()
		d.log.Error("calibration failed", "err", runErr)
	} else {
		d.log.Info("calibration finished", "outcome", rec.Outcome, "reason", rec.Reason,
			"energy_full_before_uwh", rec.EnergyFullBeforeUWh, "energy_full_after_uwh", rec.EnergyFullAfterUWh)
	}

	// Record and persist before clearing the running flag, so that once a run
	// is observable as finished its result is already durable on disk (and the
	// control socket cannot report "not running" with no saved outcome yet).
	d.mu.Lock()
	d.st.LastCalibration = &rec
	st := d.st
	d.mu.Unlock()

	if err := state.Save(d.statePath, st); err != nil {
		d.log.Error("save state failed", "err", err)
	}

	d.mu.Lock()
	d.running = false
	d.cancel = nil
	d.runReason = ""
	d.mu.Unlock()
}

// Cancel stops a running calibration; the engine restores the resting band as
// it unwinds. It is a no-op when nothing is running.
func (d *Daemon) Cancel() error {
	d.mu.Lock()
	cancel := d.cancel
	d.mu.Unlock()
	if cancel == nil {
		return fmt.Errorf("no calibration is running")
	}
	cancel()
	return nil
}

// SetBand changes the resting band at runtime and re-asserts it when idle. It
// is not persisted to the config file; edit the file for a permanent change.
func (d *Daemon) SetBand(start, stop int) error {
	d.mu.Lock()
	next := d.cfg
	next.BandStart, next.BandStop = start, stop
	if err := next.Validate(); err != nil {
		d.mu.Unlock()
		return err
	}
	d.cfg = next
	running := d.running
	d.mu.Unlock()

	if running {
		return nil // the running calibration will restore to the new band on exit
	}
	return policy.AssertBand(d.bat, start, stop)
}

// Status gathers a snapshot for the control socket.
func (d *Daemon) Status() (control.StatusInfo, error) {
	r, err := d.bat.Read()
	if err != nil {
		return control.StatusInfo{}, fmt.Errorf("battery read: %w", err)
	}
	beh, err := d.bat.Behaviour()
	if err != nil {
		return control.StatusInfo{}, fmt.Errorf("read behaviour: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	info := control.StatusInfo{
		Capacity:            r.Capacity,
		Status:              string(r.Status),
		BehaviourActive:     string(beh),
		BandStart:           d.cfg.BandStart,
		BandStop:            d.cfg.BandStop,
		HasThresholds:       d.profile.HasThresholds,
		EnergyFullUWh:       r.EnergyFullUWh,
		EnergyFullDesignUWh: r.EnergyFullDesignUWh,
		CapsSummary:         d.profile.Summary(),
		CalibrationRunning:  d.running,
		LastCalibration:     d.st.LastCalibration,
	}
	return info, nil
}

// Server is the control surface Serve drives: control.Server satisfies it.
type Server interface {
	Serve() error
	Close() error
}

// Serve runs the daemon's main loop: it starts the control server, checks
// whether calibration is due at startup and on every tick, and shuts down
// cleanly when ctx is cancelled or the server stops. ticks is the due-check
// heartbeat (a time.Ticker channel in production); now supplies the current
// time for the due decision. It returns the server's error, if any.
func (d *Daemon) Serve(ctx context.Context, srv Server, ticks <-chan time.Time, now func() time.Time) error {
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve() }()

	d.Tick(now())
	for {
		select {
		case <-ctx.Done():
			_ = srv.Close()
			d.Shutdown()
			return nil
		case err := <-srvErr:
			d.Shutdown()
			return err
		case <-ticks:
			d.Tick(now())
		}
	}
}

// Shutdown cancels any running calibration and waits for it to unwind (which
// restores the resting band), so the daemon never exits mid-cycle.
func (d *Daemon) Shutdown() {
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel()
	}
	d.mu.Unlock()
	d.wg.Wait()
}
