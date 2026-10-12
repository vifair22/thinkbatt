// Package policy holds the two decisions that do not belong to the hardware
// layer or the calibration state machine: asserting the resting charge band,
// and deciding when a calibration is due. Keeping them here as plain functions
// makes both trivially testable.
package policy

import (
	"fmt"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// AssertBand puts the pack into its normal resting state: the charge band (when
// the machine has thresholds) and auto charge behaviour. It is called at
// startup and after every calibration, and is the single definition of "safe
// resting state" the daemon returns to.
func AssertBand(b battery.Battery, start, stop int) error {
	if b.HasThresholds() {
		if err := b.SetThresholds(start, stop); err != nil {
			return fmt.Errorf("set band %d/%d: %w", start, stop, err)
		}
	}
	if err := b.SetBehaviour(battery.BehaviourAuto); err != nil {
		return fmt.Errorf("set behaviour auto: %w", err)
	}
	return nil
}

// Due reports whether a calibration should run now, and why. It fires when
// there has never been one, when at least everyCycles charge cycles have passed
// since the last, or when at least everyDays days have. The cycle arm catches a
// heavily used pack; the time arm catches a pack that barely cycles so the
// gauge does not drift untouched forever.
func Due(last *state.Calibration, r battery.Reading, now time.Time, everyCycles, everyDays int) (bool, string) {
	if last == nil {
		return true, "no prior calibration"
	}
	if cycles := r.CycleCount - last.CycleCountAt; cycles >= everyCycles {
		return true, fmt.Sprintf("%d cycles since last calibration (threshold %d)", cycles, everyCycles)
	}
	if days := int(now.Sub(last.FinishedAt).Hours() / 24); days >= everyDays {
		return true, fmt.Sprintf("%d days since last calibration (threshold %d)", days, everyDays)
	}
	return false, "not due"
}
