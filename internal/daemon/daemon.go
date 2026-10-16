// Package daemon is thinkbatt's orchestration: it discovers the battery packs
// present, asserts each one's resting band, decides when a pack's calibration
// is due, runs at most one calibration at a time against a chosen pack, and
// answers the control socket. It is split out from cmd/thinkbattd so the policy
// and concurrency can be tested against simulated packs and a virtual clock.
// Flag parsing, signal handling, and sleep inhibition stay in the command.
package daemon

import (
	"context"
	"errors"
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
	cancel      context.CancelCauseFunc
	runDone     chan struct{} // closed once the run has unwound and the band is re-asserted
	engine      *calib.Engine // the running engine, for its phase; nil when idle
	runReason   string
	wg          sync.WaitGroup

	// saveMu serialises state snapshots and their writes, so an older snapshot
	// can never be renamed over a newer one.
	saveMu sync.Mutex
}

// Causes recorded as the reason when a calibration is cancelled.
var (
	errCancelUser     = errors.New("cancelled by user")
	errCancelOverride = errors.New("cancelled for a charge override")
	errCancelShutdown = errors.New("cancelled by daemon shutdown")
)

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

// bandForLocked is the band a pack should be held at now: its override's target
// while one is active, otherwise its resting band. The caller holds d.mu.
func (d *Daemon) bandForLocked(m *managed) (start, stop int) {
	if o := d.st.Override(m.name); o != nil {
		return policy.OverrideBand(o.Target)
	}
	return m.cfg.BandStart, m.cfg.BandStop
}

// assertBands puts every present, band-capable pack into the band it should be
// at (bandForLocked), skipping the pack currently being calibrated (the
// calibration owns its pack's behaviour until it unwinds). Called at startup, on
// every maintenance tick, and when a calibration or override ends, so a freshly
// docked pack is banded promptly and a firmware reset is undone.
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
			start, stop := d.bandForLocked(m)
			todo = append(todo, band{m, start, stop})
		}
	}
	d.mu.Unlock()

	for _, b := range todo {
		if err := policy.AssertBand(b.m.bat, b.start, b.stop); err != nil {
			d.log.Warn("assert band failed", "pack", b.m.name, "err", err)
		}
	}
}

// Maintain is the periodic pass: rediscover packs, end any override that is
// over, re-assert bands, then start a calibration if one pack is due and none is
// running. Production fires it on a ticker; tests call it directly with a chosen
// now.
func (d *Daemon) Maintain(now time.Time) {
	d.Refresh()
	d.expireOverrides(now)
	d.assertBands()
	d.dueCheck(now)
}

// expireOverrides drops every override that has ended. A deadline override ends
// whether or not its pack is present, so one set on a since-pulled pack does not
// linger; an until-full override needs a reading, so it waits for its pack.
// The caller re-asserts bands afterwards, which returns the pack to its resting
// band.
func (d *Daemon) expireOverrides(now time.Time) {
	d.mu.Lock()
	ended := false
	for name, o := range d.st.Overrides {
		var r battery.Reading
		if o.UntilFull {
			m, ok := d.packs[name]
			if !ok {
				continue
			}
			var err error
			if r, err = m.bat.Read(); err != nil {
				d.log.Warn("override check: battery read failed", "pack", name, "err", err)
				continue
			}
		}
		if done, why := policy.OverrideEnded(*o, r, now); done {
			d.st.SetOverride(name, nil)
			ended = true
			d.log.Info("charge override ended", "pack", name, "target", o.Target, "reason", why)
		}
	}
	d.mu.Unlock()
	if ended {
		d.persist()
	}
}

// persist saves a snapshot of the state. Errors are logged, not returned: the
// in-memory state stays authoritative and the next change retries the write.
func (d *Daemon) persist() {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	st := d.st.Clone()
	d.mu.Unlock()
	if err := state.Save(d.statePath, st); err != nil {
		d.log.Error("save state failed", "err", err)
	}
}

// dueCheck starts a calibration on the first pack (in name order) that is
// enabled, capable, and due, provided none is already running. One at a time:
// the force-discharge assist and the sleep inhibitor are shared, and two packs
// cannot sensibly force-discharge at once. A pack under a charge override is
// skipped: draining it to the floor is the opposite of what the override asked.
func (d *Daemon) dueCheck(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return
	}
	for _, name := range d.sortedNamesLocked() {
		m := d.packs[name]
		if !m.cfg.CalibEnabled || !m.profile.CanCalibrate() || d.st.Override(name) != nil {
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
	if d.st.Override(m.name) != nil {
		return fmt.Errorf("pack %s has a charge override; clear it first with `thinkbatt charge reset`", m.name)
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
	ctx, cancel := context.WithCancelCause(context.Background())
	d.running = true
	d.runningPack = m.name
	d.cancel = cancel
	d.runDone = make(chan struct{})
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
	go d.run(ctx, cancel, d.runDone, m, params)
}

func (d *Daemon) run(ctx context.Context, cancel context.CancelCauseFunc, done chan struct{}, m *managed, params calib.Params) {
	defer d.wg.Done()
	defer close(done)
	defer cancel(nil)

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
	d.mu.Lock()
	d.engine = engine
	d.mu.Unlock()
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
	d.mu.Unlock()
	d.persist()

	d.mu.Lock()
	d.running = false
	d.runningPack = ""
	d.cancel = nil
	d.runDone = nil
	d.engine = nil
	d.runReason = ""
	d.mu.Unlock()

	// The engine restored the band it was started with; re-assert now so a band
	// changed during the run, or an override that cancelled it, applies at once
	// rather than on the next tick.
	d.assertBands()
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
	cancel(errCancelUser)
	return nil
}

// SetBand changes the resting band at runtime and re-asserts it. An empty
// battery name applies to every present band-capable pack; a name targets one.
// It is not persisted; edit the config for a permanent change. A pack being
// calibrated keeps its new band in memory and gets it when the run ends. A pack
// under a charge override keeps the override and returns to the new band when
// it ends.
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
	type band struct {
		m           *managed
		start, stop int
	}
	var todo []band
	for _, m := range targets {
		m.cfg.BandStart, m.cfg.BandStop = start, stop
		if m.name == runningPack {
			continue // the run re-asserts bands when it ends
		}
		bs, be := d.bandForLocked(m)
		todo = append(todo, band{m, bs, be})
	}
	d.mu.Unlock()

	var firstErr error
	for _, b := range todo {
		if err := policy.AssertBand(b.m.bat, b.start, b.stop); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// bandTargetsLocked resolves a battery name to the packs a band-level command
// acts on: every present band-capable pack for an empty name, or the named pack,
// which must be present and band-capable. The caller holds d.mu.
func (d *Daemon) bandTargetsLocked(batteryName string) ([]*managed, error) {
	if batteryName == "" {
		var targets []*managed
		for _, name := range d.sortedNamesLocked() {
			if d.packs[name].profile.CanManageBand() {
				targets = append(targets, d.packs[name])
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("no band-capable pack present")
		}
		return targets, nil
	}
	m, ok := d.packs[batteryName]
	if !ok {
		return nil, fmt.Errorf("no such pack %q; present: %s", batteryName, strings.Join(d.sortedNamesLocked(), ", "))
	}
	if !m.profile.CanManageBand() {
		return nil, fmt.Errorf("pack %s has no charge thresholds to override", m.name)
	}
	return []*managed{m}, nil
}

// SetOverride holds packs at target until the deadline, or until full when
// untilFull is set, replacing any override they already had. An empty battery
// name applies to every present band-capable pack. A calibration running on an
// affected pack is cancelled first and waited out, so the engine's own restore
// of the resting band cannot land after the override and undo it. The override
// is persisted, so it survives a restart.
func (d *Daemon) SetOverride(batteryName string, target int, until time.Time, untilFull bool) error {
	if target < 2 || target > 100 {
		return fmt.Errorf("charge target %d%% invalid: need 2-100", target)
	}
	now := d.clock.Now()
	if !untilFull && !until.After(now) {
		return fmt.Errorf("charge override deadline %s is not in the future", until.Format("2006-01-02 15:04"))
	}

	d.mu.Lock()
	targets, err := d.bandTargetsLocked(batteryName)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	var cancel context.CancelCauseFunc
	var done chan struct{}
	for _, m := range targets {
		o := &state.Override{Target: target, UntilFull: untilFull, SetAt: now}
		if !untilFull {
			o.Until = until
		}
		d.st.SetOverride(m.name, o)
		if d.running && d.runningPack == m.name {
			cancel, done = d.cancel, d.runDone
		}
		d.log.Info("charge override set", "pack", m.name, "target", target,
			"until", overrideUntil(o))
	}
	d.mu.Unlock()

	if cancel != nil {
		cancel(errCancelOverride)
		<-done
	}
	d.persist()
	d.assertBands()
	return nil
}

// ClearOverride ends overrides early. An empty battery name clears every
// override; a name clears that pack's, which must have one.
func (d *Daemon) ClearOverride(batteryName string) error {
	d.mu.Lock()
	switch {
	case batteryName == "" && len(d.st.Overrides) == 0:
		d.mu.Unlock()
		return fmt.Errorf("no charge override is active")
	case batteryName == "":
		for name := range d.st.Overrides {
			d.st.SetOverride(name, nil)
		}
	case d.st.Override(batteryName) == nil:
		d.mu.Unlock()
		return fmt.Errorf("pack %s has no charge override", batteryName)
	default:
		d.st.SetOverride(batteryName, nil)
	}
	d.mu.Unlock()
	cleared := batteryName
	if cleared == "" {
		cleared = "all"
	}
	d.log.Info("charge override cleared", "pack", cleared)

	d.persist()
	d.assertBands()
	return nil
}

func overrideUntil(o *state.Override) string {
	if o.UntilFull {
		return "full"
	}
	return o.Until.Format(time.RFC3339)
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
	phase := ""
	if d.engine != nil {
		phase = d.engine.Phase()
	}
	snap := d.st.Clone()
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
			PowerUW:             signedPower(r),
			VoltageUV:           r.VoltageNowUV,
			EnergyFullUWh:       r.EnergyFullUWh,
			EnergyFullDesignUWh: r.EnergyFullDesignUWh,
			CapsSummary:         m.profile.Summary(),
			CalibrationRunning:  running && runningPack == m.name,
			LastCalibration:     snap.Last(m.name),
			Override:            snap.Override(m.name),
		})
		if running && runningPack == m.name {
			info.Packs[len(info.Packs)-1].CalibrationPhase = phase
		}
	}
	return info, nil
}

// signedPower turns the reading's power magnitude into a flow into the pack:
// negative when discharging, so a client need not interpret Status for it.
func signedPower(r battery.Reading) int64 {
	if r.Status == battery.StatusDischarging {
		return -r.PowerNowUW
	}
	return r.PowerNowUW
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
		d.cancel(errCancelShutdown)
	}
	d.mu.Unlock()
	d.wg.Wait()
}
