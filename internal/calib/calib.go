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

// Params configures a run. Timeouts bound each waiting phase so a stuck pack
// cannot hang the engine forever.
type Params struct {
	BandStart int
	BandStop  int
	Floor     int // discharge target, percent

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
}

// New builds an Engine. A nil clock uses the real clock; a nil logger discards.
func New(bat battery.Battery, clock Clock, log *slog.Logger, p Params) *Engine {
	if clock == nil {
		clock = RealClock{}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	p.withDefaults()
	return &Engine{bat: bat, clock: clock, log: log, params: p}
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

	// The safety net: whatever happens below, leave the pack safe. Uses a
	// background context so it still writes sysfs after ctx cancellation; the
	// writes are immediate and do not block.
	defer func() {
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

	phaseErr := e.cycle(ctx)
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

// cycle is the charge -> discharge -> recharge -> drain-to-band sequence.
func (e *Engine) cycle(ctx context.Context) error {
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

	e.log.Info("calibration: discharging to floor", "floor", p.Floor)
	if err := e.bat.SetBehaviour(battery.BehaviourForceDischarge); err != nil {
		return fmt.Errorf("set force-discharge: %w", err)
	}
	if err := e.waitFloor(ctx, p.Floor, p.DischargeTimeout); err != nil {
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
	if err := e.drainToBand(ctx, p.BandStop, p.DischargeTimeout); err != nil {
		return err
	}
	return nil
}

// waitFull polls until the pack reports full. It aborts if AC is lost, since a
// charge cannot complete on battery.
func (e *Engine) waitFull(ctx context.Context, timeout time.Duration) error {
	return e.poll(ctx, timeout, "charge", func(r battery.Reading) (bool, error) {
		if !r.ACOnline {
			return false, errAbort{"AC lost during charge"}
		}
		return r.Capacity >= 99 && r.Status != battery.StatusCharging, nil
	})
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
func (e *Engine) drainToBand(ctx context.Context, stop int, timeout time.Duration) error {
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
