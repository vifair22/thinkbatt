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

// OverrideBand is the threshold pair that charges a pack to target. The start
// threshold sits one point below the stop: charging only resumes below start,
// so a pack at 78% under a 75/100 band would never top up, which is the
// opposite of what an override asks for.
func OverrideBand(target int) (start, stop int) {
	return target - 1, target
}

// overrideNearMark is how far below the target a stopped charger still counts
// as having reached it. An EC may decline the last percent or two (with a 99/100
// band a gauge reading 99 never resumes charging), the same quirk calib's
// fullMark absorbs.
const overrideNearMark = 3

// overrideSettle is how long after an override is set before a stopped charger
// near the target is believed. Right after the thresholds are raised the pack
// can still report the "Not charging" it held under the old band; the EC needs a
// moment to start the charge the new band calls for.
const overrideSettle = 2 * time.Minute

// OverrideEnded reports whether an override is over, and why. A deadline
// override ends at its deadline. An until-full override ends once the pack
// reaches the target, or sits on AC with the charger stopped within
// overrideNearMark of it after the settle time; on battery it waits, since
// unplugging mid-charge does not mean the pack is full.
func OverrideEnded(o state.Override, r battery.Reading, now time.Time) (bool, string) {
	if !o.UntilFull {
		if !now.Before(o.Until) {
			return true, "deadline reached"
		}
		return false, ""
	}
	if r.Capacity >= o.Target {
		return true, fmt.Sprintf("reached %d%%", r.Capacity)
	}
	settled := now.Sub(o.SetAt) >= overrideSettle
	if settled && r.ACOnline && r.Status != battery.StatusCharging && r.Capacity >= o.Target-overrideNearMark {
		return true, fmt.Sprintf("charger stopped at %d%%", r.Capacity)
	}
	return false, ""
}
