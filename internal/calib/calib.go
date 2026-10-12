// Package calib runs a fuel-gauge learning cycle: charge the pack full,
// force-discharge it to a floor, recharge it full, then return it to the
// resting band. Completing a full charge/discharge/charge gives the pack's
// gauge the endpoints it needs to re-anchor its learned full-charge capacity,
// which a pack held in a narrow band never sees.
//
// The engine drives only the battery; it takes a Clock so a test can run the
// multi-hour sequence in virtual time against a simulator. Sleep inhibition and
// scheduling live in the daemon, not here, so this stays pure and testable. The
// one invariant it guarantees: however Run exits - success, timeout, error, or
// context cancellation - the pack is left at auto behaviour and the resting
// band, never force-discharging or stranded at full.
package calib

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// DischargeAssist accelerates the force-discharge legs by holding the pack at a
// target power (see internal/load): idle force-discharge is too slow to reach
// the floor in a sane time. A nil assist, or a zero DischargeCRate, leaves the
// discharge at the idle draw. Start begins holding the target for ctx; Stop ends
// it and is safe to call when not started. load.Assist satisfies this.
type DischargeAssist interface {
	Start(ctx context.Context, targetWatts float64) error
	Stop()
}

// Params configures a run. Timeouts bound each waiting phase so a stuck pack
// cannot hang the engine forever.
type Params struct {
	BandStart int
	BandStop  int
	Floor     int // discharge target, percent

	// RestAtFull and RestAtFloor relax the pack in place at each turnaround so
	// the gauge can take an open-circuit-voltage reading and relearn capacity. A
	// zero duration skips that rest.
	RestAtFull  time.Duration
	RestAtFloor time.Duration

	// DischargeCRate is the discharge rate as a fraction of design capacity per
	// hour (0.2 = C/5). The engine turns it into a watt target for the assist
	// using the pack's design capacity. Zero disables the assist (idle discharge).
	DischargeCRate float64

	PollInterval     time.Duration
	ChargeTimeout    time.Duration
	DischargeTimeout time.Duration
}

// Default timeouts are generous: a full charge or a deep discharge at laptop
// power is a few hours.
const (
	defaultPoll             = 30 * time.Second
	defaultChargeTimeout    = 5 * time.Hour
	defaultDischargeTimeout = 6 * time.Hour
)

// fullMark is the state of charge at or above which a stopped charger counts as
// a completed full charge. It sits a few points below 100 to absorb fuel-gauge
// rounding and the last percent or two an EC may decline to top off: with
// end=100 the highest usable start threshold is 99, the very value a gauge
// rounds a near-full pack up to, so the EC will not resume charging and Linux
// never reports 99. Treating a stopped charger near full as done lets a
// calibration that begins near full proceed instead of waiting out the timeout.
const fullMark = 97

func (p *Params) withDefaults() {
	if p.PollInterval <= 0 {
		p.PollInterval = defaultPoll
	}
	if p.ChargeTimeout <= 0 {
		p.ChargeTimeout = defaultChargeTimeout
	}
	if p.DischargeTimeout <= 0 {
		p.DischargeTimeout = defaultDischargeTimeout
	}
}

// Engine runs calibrations against a battery.
type Engine struct {
	bat    battery.Battery
	clock  Clock
	log    *slog.Logger
	params Params
	assist DischargeAssist
}

// New builds an Engine. A nil clock uses the real clock; a nil logger discards.
// A nil assist runs the discharge legs at the idle draw.
func New(bat battery.Battery, clock Clock, log *slog.Logger, p Params, assist DischargeAssist) *Engine {
	if clock == nil {
		clock = RealClock{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p.withDefaults()
	return &Engine{bat: bat, clock: clock, log: log, params: p, assist: assist}
}

// Result summarises a run for the state file and logs.
type Result struct {
	Outcome             state.Outcome
	Reason              string
	CycleCount          int
	EnergyFullBeforeUWh int64
	EnergyFullAfterUWh  int64
}

// errAbort carries a human reason for a non-fatal abort (e.g. AC lost). Run
// turns it into an aborted Result rather than an error return.
type errAbort struct{ reason string }

func (e errAbort) Error() string { return e.reason }

// Run executes the learning cycle. It returns a Result describing the outcome;
// it returns a non-nil error only for an unexpected failure (a sysfs error),
// not for an orderly abort or a context cancellation, which are reported in the
// Result. The resting band is always restored before Run returns.
func (e *Engine) Run(ctx context.Context) (res Result, err error) {
	before, err := e.bat.Read()
	if err != nil {
		return Result{}, fmt.Errorf("initial read: %w", err)
	}
	res.EnergyFullBeforeUWh = before.EnergyFullUWh
	res.CycleCount = before.CycleCount

	// targetW is the discharge power the assist holds, derived from the C-rate
	// and the pack's design capacity. Zero (no C-rate, no design, or no assist)
	// means the discharge runs at the idle draw.
	targetW := 0.0
	if e.params.DischargeCRate > 0 && before.EnergyFullDesignUWh > 0 {
		targetW = e.params.DischargeCRate * float64(before.EnergyFullDesignUWh) / 1e6
	}

	// The safety net: whatever happens below, leave the pack safe. Uses a
	// background context so it still writes sysfs after ctx cancellation; the
	// writes are immediate and do not block. Stop the load first so nothing
	// keeps discharging while the band is restored.
	defer func() {
		if e.assist != nil {
			e.assist.Stop()
		}
		if rerr := e.restore(); rerr != nil {
			e.log.Error("restore to resting band failed", "err", rerr)
			if err == nil && res.Outcome == state.OutcomeOK {
				// A failed restore downgrades a nominal success.
				res.Outcome = state.OutcomeFailed
				res.Reason = "restore failed: " + rerr.Error()
			}
		}
	}()

	if !before.ACOnline {
		res.Outcome, res.Reason = state.OutcomeAborted, "AC not connected"
		return res, nil
	}
	if supported, serr := e.bat.Supported(); serr != nil {
		return res, fmt.Errorf("probe behaviours: %w", serr)
	} else if !battery.Supports(supported, battery.BehaviourForceDischarge) {
		res.Outcome, res.Reason = state.OutcomeAborted, "force-discharge not supported"
		return res, nil
	}

	phaseErr := e.cycle(ctx, targetW)
	switch {
	case phaseErr == nil:
		res.Outcome, res.Reason = state.OutcomeOK, "learning cycle complete"
	case errors.Is(phaseErr, context.Canceled), errors.Is(phaseErr, context.DeadlineExceeded):
		res.Outcome, res.Reason = state.OutcomeAborted, "cancelled"
	default:
		var ab errAbort
		if errors.As(phaseErr, &ab) {
			res.Outcome, res.Reason = state.OutcomeAborted, ab.reason
		} else {
			res.Outcome, res.Reason = state.OutcomeFailed, phaseErr.Error()
		}
	}

	if after, aerr := e.bat.Read(); aerr == nil {
		res.EnergyFullAfterUWh = after.EnergyFullUWh
		res.CycleCount = after.CycleCount
	}
	return res, nil
}

// cycle is the full learning sequence: charge -> rest -> discharge -> rest ->
// recharge -> drain-to-band. The two rests let the gauge relax and take the
// open-circuit-voltage readings it needs to relearn capacity; the discharge
// legs are held at targetW by the assist when one is configured.
func (e *Engine) cycle(ctx context.Context, targetW float64) error {
	p := e.params

	e.log.Info("calibration: charging to full")
	if err := e.bat.SetThresholds(99, 100); err != nil {
		return fmt.Errorf("set charge-to-full thresholds: %w", err)
	}
	if err := e.bat.SetBehaviour(battery.BehaviourAuto); err != nil {
		return fmt.Errorf("set auto for charge: %w", err)
	}
	if err := e.waitFull(ctx, p.ChargeTimeout); err != nil {
		return err
	}

	if err := e.rest(ctx, "full", p.RestAtFull); err != nil {
		return err
	}

	e.log.Info("calibration: discharging to floor", "floor", p.Floor, "target_w", targetW)
	if err := e.bat.SetBehaviour(battery.BehaviourForceDischarge); err != nil {
		return fmt.Errorf("set force-discharge: %w", err)
	}
	stopAssist := e.startAssist(ctx, targetW)
	errFloor := e.waitFloor(ctx, p.Floor, p.DischargeTimeout)
	stopAssist()
	if errFloor != nil {
		return errFloor
	}

	if err := e.rest(ctx, "floor", p.RestAtFloor); err != nil {
		return err
	}

	e.log.Info("calibration: recharging to full")
	if err := e.bat.SetBehaviour(battery.BehaviourAuto); err != nil {
		return fmt.Errorf("set auto for recharge: %w", err)
	}
	if err := e.waitFull(ctx, p.ChargeTimeout); err != nil {
		return err
	}

	e.log.Info("calibration: draining to resting band", "stop", p.BandStop)
	if err := e.drainToBand(ctx, p.BandStop, targetW, p.DischargeTimeout); err != nil {
		return err
	}
	return nil
}

// rest relaxes the pack in place so the gauge can take an open-circuit-voltage
// reading: inhibit-charge holds the pack neither charging nor discharging on AC.
// A zero duration skips the rest; a machine without inhibit-charge skips it with
// a warning (the relearn is weaker but the cycle still runs). AC loss aborts,
// since without AC the pack would drain through the rest.
func (e *Engine) rest(ctx context.Context, where string, dur time.Duration) error {
	if dur <= 0 {
		return nil
	}
	supported, err := e.bat.Supported()
	if err != nil {
		return fmt.Errorf("rest at %s: probe behaviours: %w", where, err)
	}
	if !battery.Supports(supported, battery.BehaviourInhibitCharge) {
		e.log.Warn("inhibit-charge unsupported; skipping relaxation rest (weaker relearn)", "at", where)
		return nil
	}
	e.log.Info("calibration: resting", "at", where, "for", dur)
	if err := e.bat.SetBehaviour(battery.BehaviourInhibitCharge); err != nil {
		return fmt.Errorf("set inhibit-charge for %s rest: %w", where, err)
	}
	deadline := e.clock.Now().Add(dur)
	for e.clock.Now().Before(deadline) {
		r, err := e.bat.Read()
		if err != nil {
			return fmt.Errorf("rest at %s: read: %w", where, err)
		}
		if !r.ACOnline {
			return errAbort{"AC lost during " + where + " rest"}
		}
		if err := e.clock.Sleep(ctx, e.params.PollInterval); err != nil {
			return err
		}
	}
	return nil
}

// startAssist begins the controlled discharge load if an assist is configured
// and a target is set. It always returns a stop func that is safe to call.
func (e *Engine) startAssist(ctx context.Context, targetW float64) func() {
	if e.assist == nil || targetW <= 0 {
		return func() {}
	}
	if err := e.assist.Start(ctx, targetW); err != nil {
		e.log.Warn("discharge assist failed to start; continuing at idle draw", "err", err)
		return func() {}
	}
	return e.assist.Stop
}

// waitFull polls until the pack reports full. It aborts if AC is lost, since a
// charge cannot complete on battery.
func (e *Engine) waitFull(ctx context.Context, timeout time.Duration) error {
	return e.poll(ctx, timeout, "charge", func(r battery.Reading) (bool, error) {
		if !r.ACOnline {
			return false, errAbort{"AC lost during charge"}
		}
		return chargeComplete(r), nil
	})
}

// chargeComplete reports whether a charge-to-full phase is done. The charger has
// stopped at the top when the pack is no longer Charging (the EC reports Full,
// or Not charging at its end threshold); a pack already near full will not
// resume charging at all, so a stopped charger at or above fullMark counts as
// full. A brief not-charging pause at a lower state of charge is not full.
func chargeComplete(r battery.Reading) bool {
	return r.Status != battery.StatusCharging && r.Capacity >= fullMark
}

// waitFloor polls a force-discharge until the pack falls to the floor. AC loss
// aborts: the user pulled the adapter, so the run stops and restores.
func (e *Engine) waitFloor(ctx context.Context, floor int, timeout time.Duration) error {
	return e.poll(ctx, timeout, "discharge", func(r battery.Reading) (bool, error) {
		if !r.ACOnline {
			return false, errAbort{"AC lost during discharge"}
		}
		return r.Capacity <= floor, nil
	})
}

// drainToBand force-discharges from full back down to the resting ceiling, so
// the pack does not coast at a high charge for days after the cycle. If it is
// already at or below the ceiling there is nothing to do.
func (e *Engine) drainToBand(ctx context.Context, stop int, targetW float64, timeout time.Duration) error {
	r, err := e.bat.Read()
	if err != nil {
		return fmt.Errorf("read before drain: %w", err)
	}
	if r.Capacity <= stop {
		return nil
	}
	if err := e.bat.SetBehaviour(battery.BehaviourForceDischarge); err != nil {
		return fmt.Errorf("set force-discharge for drain: %w", err)
	}
	stopAssist := e.startAssist(ctx, targetW)
	defer stopAssist()
	return e.poll(ctx, timeout, "drain", func(r battery.Reading) (bool, error) {
		return r.Capacity <= stop, nil
	})
}

// poll reads the battery on each PollInterval tick, calling step until it
// reports done, the timeout elapses, or ctx is cancelled.
func (e *Engine) poll(ctx context.Context, timeout time.Duration, phase string, step func(battery.Reading) (bool, error)) error {
	deadline := e.clock.Now().Add(timeout)
	for {
		r, err := e.bat.Read()
		if err != nil {
			return fmt.Errorf("%s: read: %w", phase, err)
		}
		done, err := step(r)
		if err != nil {
			return err
		}
		if done {
			return nil
		}
		if !e.clock.Now().Before(deadline) {
			return fmt.Errorf("%s: timed out after %s", phase, timeout)
		}
		if err := e.clock.Sleep(ctx, e.params.PollInterval); err != nil {
			return err
		}
	}
}

// restore returns the pack to its safe resting state: auto behaviour and the
// resting band. It writes sysfs directly (no waiting) so it works even after
// ctx cancellation, and is idempotent.
func (e *Engine) restore() error {
	var firstErr error
	if err := e.bat.SetBehaviour(battery.BehaviourAuto); err != nil {
		firstErr = fmt.Errorf("set auto: %w", err)
	}
	if e.bat.HasThresholds() {
		if err := e.bat.SetThresholds(e.params.BandStart, e.params.BandStop); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("restore band: %w", err)
		}
	}
	return firstErr
}
