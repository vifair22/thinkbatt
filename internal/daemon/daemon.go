// Package daemon is thinkbatt's orchestration: it discovers the battery packs
// present, asserts each one's resting band, decides when a pack's calibration
// is due, runs at most one calibration at a time against a chosen pack, and
// answers the control socket. It is split out from cmd/thinkbattd so the policy
// and concurrency can be tested against simulated packs and a virtual clock.
// Flag parsing, signal handling, and sleep inhibition stay in the command.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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

// AssistFactory builds a discharge assist bound to one pack's power_now
// feedback, called when a calibration starts on that pack. A nil factory, or a
// factory returning nil, runs the discharge at idle draw. It exists because the
// assist's measurement must read the pack being calibrated, which is not known
// until a run begins.
type AssistFactory func(bat battery.Battery) calib.DischargeAssist

// managed is one pack the daemon owns: its hardware handle, detected
// capabilities, and resolved per-pack policy.
type managed struct {
	name    string
	bat     battery.Battery
	profile caps.Profile
	cfg     config.Config
}

// Options configures a Daemon.
type Options struct {
	Set       battery.Set   // enumerates and opens the packs
	File      config.File   // global policy plus per-pack overrides
	StatePath string        //
	Clock     calib.Clock   // engine clock; nil uses the real clock
	Inhibitor Inhibitor     // nil uses a no-op
	Assist    AssistFactory // per-run discharge assist; nil runs discharge at idle draw
	Logger    *slog.Logger  //
}

// Daemon holds the live state behind the control socket.
type Daemon struct {
	set       battery.Set
	file      config.File
	statePath string
	clock     calib.Clock
	inhibitor Inhibitor
	assist    AssistFactory
	log       *slog.Logger

	mu          sync.Mutex
	packs       map[string]*managed
	st          state.State
	running     bool
	runningPack string
	cancel      context.CancelFunc
	runReason   string
	wg          sync.WaitGroup
}

// New builds a Daemon and loads any persisted state. It does not touch hardware
// yet; call Refresh (or Serve, which calls it) to discover packs.
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
		set:       opts.Set,
		file:      opts.File,
		statePath: opts.StatePath,
		clock:     opts.Clock,
		inhibitor: opts.Inhibitor,
		assist:    opts.Assist,
		log:       opts.Logger,
		packs:     map[string]*managed{},
		st:        st,
	}, nil
}

// Refresh reconciles the managed set with the packs present now: it adds any
// newly-seen pack (detecting its capabilities and resolving its policy) and
// drops any that has gone away. It does no sysfs writes; assertBands applies the
// band. A pack that fails capability detection is skipped this round and
// retried on the next.
func (d *Daemon) Refresh() {
	names, err := d.set.List()
	if err != nil {
		d.log.Warn("pack discovery failed", "err", err)
		return
	}
	present := make(map[string]bool, len(names))
	for _, n := range names {
		present[n] = true
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	for _, name := range names {
		if _, ok := d.packs[name]; ok {
			continue
		}
		bat := d.set.Open(name)
		profile, err := caps.Detect(bat)
		if err != nil {
			d.log.Warn("capability detection failed; will retry", "pack", name, "err", err)
			continue
		}
		cfg, err := d.file.For(name)
		if err != nil {
			// A bad section fails at config load, so this is defensive; fall back
			// to the global policy rather than refuse to manage the pack.
			d.log.Warn("per-pack config failed to resolve; using global", "pack", name, "err", err)
			cfg = d.file.Global
		}
		d.packs[name] = &managed{name: name, bat: bat, profile: profile, cfg: cfg}
		d.log.Info("pack discovered", "pack", name, "caps", profile.Summary(),
			"band", fmt.Sprintf("%d/%d", cfg.BandStart, cfg.BandStop))
	}
	for name := range d.packs {
		if !present[name] {
			delete(d.packs, name)
			d.log.Info("pack removed", "pack", name)
		}
	}
}

// assertBands puts every present, band-capable pack into its resting band,
// skipping the pack currently being calibrated (the calibration owns its pack's
// behaviour until it unwinds). Called at startup and on every maintenance tick,
// so a freshly docked pack is banded promptly and a firmware reset is undone.
func (d *Daemon) assertBands() {
	d.mu.Lock()
	runningPack := d.runningPack
	type band struct {
		m           *managed
		start, stop int
	}
	var todo []band
	for _, m := range d.packs {
		if m.profile.CanManageBand() && m.name != runningPack {
			todo = append(todo, band{m, m.cfg.BandStart, m.cfg.BandStop})
		}
	}
	d.mu.Unlock()

	for _, b := range todo {
		if err := policy.AssertBand(b.m.bat, b.start, b.stop); err != nil {
			d.log.Warn("assert band failed", "pack", b.m.name, "err", err)
		}
	}
}

// Maintain is the periodic pass: rediscover packs, re-assert bands, then start a
// calibration if one pack is due and none is running. Production fires it on a
// ticker; tests call it directly with a chosen now.
func (d *Daemon) Maintain(now time.Time) {
	d.Refresh()
	d.assertBands()
	d.dueCheck(now)
}

// dueCheck starts a calibration on the first pack (in name order) that is
// enabled, capable, and due, provided none is already running. One at a time:
// the force-discharge assist and the sleep inhibitor are shared, and two packs
// cannot sensibly force-discharge at once.
func (d *Daemon) dueCheck(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return
	}
	for _, name := range d.sortedNamesLocked() {
		m := d.packs[name]
		if !m.cfg.CalibEnabled || !m.profile.CanCalibrate() {
			continue
		}
		r, err := m.bat.Read()
		if err != nil {
			d.log.Warn("due-check: battery read failed", "pack", name, "err", err)
			continue
		}
		due, reason := policy.Due(d.st.Last(name), r, now, m.cfg.CalibEveryCycles, m.cfg.CalibEveryDays)
		if due {
			d.startLocked(m, reason)
			return
		}
	}
}

// Calibrate requests a run on a chosen pack. battery names the pack; an empty
// name is allowed only when exactly one pack is present. now bypasses the
// due-check.
func (d *Daemon) Calibrate(batteryName string, now bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return fmt.Errorf("a calibration is already running on %s", d.runningPack)
	}
	m, err := d.resolveLocked(batteryName)
	if err != nil {
		return err
	}
	if !m.profile.CanCalibrate() {
		return fmt.Errorf("pack %s cannot calibrate: %s", m.name, m.profile.Summary())
	}
	if !now {
		r, err := m.bat.Read()
		if err != nil {
			return fmt.Errorf("battery read: %w", err)
		}
		due, reason := policy.Due(d.st.Last(m.name), r, d.clock.Now(), m.cfg.CalibEveryCycles, m.cfg.CalibEveryDays)
		if !due {
			return fmt.Errorf("%s not due: %s (use --now to force)", m.name, reason)
		}
		d.startLocked(m, reason)
		return nil
	}
	d.startLocked(m, "manual")
	return nil
}

// resolveLocked maps a requested battery name to a managed pack. An empty name
// resolves to the sole pack when there is exactly one, otherwise it is an error
// that lists the choices, so a calibration never lands on a pack by accident.
// The caller holds d.mu.
func (d *Daemon) resolveLocked(name string) (*managed, error) {
	if name != "" {
		m, ok := d.packs[name]
		if !ok {
			return nil, fmt.Errorf("no such pack %q; present: %s", name, strings.Join(d.sortedNamesLocked(), ", "))
		}
		return m, nil
	}
	names := d.sortedNamesLocked()
	switch len(names) {
	case 0:
		return nil, fmt.Errorf("no battery packs present")
	case 1:
		return d.packs[names[0]], nil
	default:
		return nil, fmt.Errorf("multiple packs present (%s); specify which with --battery", strings.Join(names, ", "))
	}
}

func (d *Daemon) sortedNamesLocked() []string {
	names := make([]string, 0, len(d.packs))
	for name := range d.packs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// startLocked launches the calibration goroutine against pack m. The caller
// holds d.mu.
func (d *Daemon) startLocked(m *managed, reason string) {
	ctx, cancel := context.WithCancel(context.Background())
	d.running = true
	d.runningPack = m.name
	d.cancel = cancel
	d.runReason = reason
	d.wg.Add(1)
	d.log.Info("calibration starting", "pack", m.name, "reason", reason)
	params := calib.Params{
		BandStart:        m.cfg.BandStart,
		BandStop:         m.cfg.BandStop,
		Floor:            m.cfg.CalibFloor,
		RestAtFull:       time.Duration(m.cfg.RestAtFullMinutes) * time.Minute,
		RestAtFloor:      time.Duration(m.cfg.RestAtFloorMinutes) * time.Minute,
		DischargeCRate:   m.cfg.DischargeCRate,
		ChargeTimeout:    time.Duration(m.cfg.ChargeTimeoutHours) * time.Hour,
		DischargeTimeout: time.Duration(m.cfg.DischargeTimeoutHours) * time.Hour,
	}
	go d.run(ctx, cancel, m, params)
}

func (d *Daemon) run(ctx context.Context, cancel context.CancelFunc, m *managed, params calib.Params) {
	defer d.wg.Done()
	defer cancel()

	release, err := d.inhibitor.Inhibit("battery calibration (" + m.name + ")")
	if err != nil {
		d.log.Warn("could not inhibit sleep; proceeding", "err", err)
		release = func() {}
	}
	defer release()

	var assist calib.DischargeAssist
	if d.assist != nil {
		assist = d.assist(m.bat)
	}

	started := d.clock.Now()
	engine := calib.New(m.bat, d.clock, d.log, params, assist)
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
		d.log.Error("calibration failed", "pack", m.name, "err", runErr)
	} else {
		d.log.Info("calibration finished", "pack", m.name, "outcome", rec.Outcome, "reason", rec.Reason,
			"energy_full_before_uwh", rec.EnergyFullBeforeUWh, "energy_full_after_uwh", rec.EnergyFullAfterUWh)
	}

	// Record and persist before clearing the running flag, so that once a run is
	// observable as finished its result is already durable on disk.
	d.mu.Lock()
	d.st.SetLast(m.name, &rec)
	st := d.st
	d.mu.Unlock()

	if err := state.Save(d.statePath, st); err != nil {
		d.log.Error("save state failed", "err", err)
	}

	d.mu.Lock()
	d.running = false
	d.runningPack = ""
	d.cancel = nil
	d.runReason = ""
	d.mu.Unlock()
}

// Cancel stops the running calibration; the engine restores that pack's resting
// band as it unwinds. It is a no-op when nothing is running.
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

// SetBand changes the resting band at runtime and re-asserts it. An empty
// battery name applies to every present band-capable pack; a name targets one.
// It is not persisted; edit the config for a permanent change. A pack being
// calibrated keeps its new band in memory and the calibration re-bands on exit.
func (d *Daemon) SetBand(batteryName string, start, stop int) error {
	if start < 1 || start > 99 || stop < 2 || stop > 100 || start >= stop {
		return fmt.Errorf("band %d/%d invalid: need 1 <= start < stop <= 100", start, stop)
	}

	d.mu.Lock()
	var targets []*managed
	if batteryName == "" {
		for _, name := range d.sortedNamesLocked() {
			if d.packs[name].profile.CanManageBand() {
				targets = append(targets, d.packs[name])
			}
		}
	} else {
		m, ok := d.packs[batteryName]
		if !ok {
			d.mu.Unlock()
			return fmt.Errorf("no such pack %q", batteryName)
		}
		targets = append(targets, m)
	}
	if len(targets) == 0 {
		d.mu.Unlock()
		return fmt.Errorf("no band-capable pack to set")
	}
	runningPack := d.runningPack
	for _, m := range targets {
		m.cfg.BandStart, m.cfg.BandStop = start, stop
	}
	d.mu.Unlock()

	var firstErr error
	for _, m := range targets {
		if m.name == runningPack {
			continue // the running calibration re-bands to the new values on exit
		}
		if err := policy.AssertBand(m.bat, start, stop); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Status gathers a per-pack snapshot for the control socket.
func (d *Daemon) Status() (control.StatusInfo, error) {
	d.mu.Lock()
	names := d.sortedNamesLocked()
	packs := make([]*managed, 0, len(names))
	for _, n := range names {
		packs = append(packs, d.packs[n])
	}
	running := d.running
	runningPack := d.runningPack
	lasts := make(map[string]*state.Calibration, len(names))
	for _, n := range names {
		lasts[n] = d.st.Last(n)
	}
	d.mu.Unlock()

	info := control.StatusInfo{}
	for _, m := range packs {
		r, err := m.bat.Read()
		if err != nil {
			return control.StatusInfo{}, fmt.Errorf("battery %s read: %w", m.name, err)
		}
		beh, err := m.bat.Behaviour()
		if err != nil {
			return control.StatusInfo{}, fmt.Errorf("battery %s behaviour: %w", m.name, err)
		}
		info.Packs = append(info.Packs, control.PackStatus{
			Name:                m.name,
			Capacity:            r.Capacity,
			Status:              string(r.Status),
			BehaviourActive:     string(beh),
			BandStart:           m.cfg.BandStart,
			BandStop:            m.cfg.BandStop,
			HasThresholds:       m.profile.HasThresholds,
			EnergyFullUWh:       r.EnergyFullUWh,
			EnergyFullDesignUWh: r.EnergyFullDesignUWh,
			CapsSummary:         m.profile.Summary(),
			CalibrationRunning:  running && runningPack == m.name,
			LastCalibration:     lasts[m.name],
		})
	}
	return info, nil
}

// Server is the control surface Serve drives: control.Server satisfies it.
type Server interface {
	Serve() error
	Close() error
}

// Serve runs the daemon's main loop: it starts the control server, runs a
// maintenance pass at startup and on every tick, and shuts down cleanly when
// ctx is cancelled or the server stops. ticks is the maintenance heartbeat (a
// time.Ticker channel in production); now supplies the current time.
func (d *Daemon) Serve(ctx context.Context, srv Server, ticks <-chan time.Time, now func() time.Time) error {
	srvErr := make(chan error, 1)
	go func() { srvErr <- srv.Serve() }()

	d.Maintain(now())
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
			d.Maintain(now())
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
