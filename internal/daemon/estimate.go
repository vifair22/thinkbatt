package daemon

import (
	"math"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
	"git.airies.net/vifair22/thinkbatt/internal/calib"
)

// powerTau is the time constant of the power average behind the time
// estimates. power_now is steady while charging but follows CPU load on
// battery, swinging several-fold between idle and a compile; a minute of memory
// keeps the runtime from jumping on every status read without lagging a real
// change in load by more than a couple of minutes.
const powerTau = 60 * time.Second

// minEstimatePowerUW is the smallest power the estimates divide by. Below it
// the pack is effectively idle (a gauge settling after the charger stops) and
// a quotient would be hours of noise rather than a prediction.
const minEstimatePowerUW = 100_000

// maxEstimate caps a reported estimate: a figure beyond it says only that the
// pack is barely moving, which is better left unsaid than shown as "97h".
const maxEstimate = 48 * time.Hour

// powerAverage is an exponentially weighted moving average of one pack's
// signed power, fed by the daemon's sampler. It restarts whenever the pack's
// status changes, so plugging in does not blend a discharge rate into the
// charge estimate.
type powerAverage struct {
	uw     float64
	status battery.Status
	at     time.Time
	valid  bool
}

// add folds a reading taken at now into the average. The weight comes from the
// time since the previous sample rather than a fixed alpha, so a sampler that
// slips, or a resume after hours suspended, weighs the new reading by the gap
// it actually covers. A clock that steps backwards restarts the average.
func (a *powerAverage) add(r battery.Reading, now time.Time) {
	p := float64(signedPower(r))
	if !a.valid || r.Status != a.status || now.Before(a.at) {
		*a = powerAverage{uw: p, status: r.Status, at: now, valid: true}
		return
	}
	alpha := 1 - math.Exp(-now.Sub(a.at).Seconds()/powerTau.Seconds())
	a.uw += alpha * (p - a.uw)
	a.at = now
}

// powerFor is the power the estimates use for reading r: the average when it
// tracks the same status, otherwise r's own instantaneous power (the sampler
// has not seen this state yet, e.g. right after plugging in or at startup).
func (a *powerAverage) powerFor(r battery.Reading) float64 {
	if a.valid && a.status == r.Status {
		return a.uw
	}
	return float64(signedPower(r))
}

// chargeTarget is the percent at which charging stops on its own: the band's
// stop (or the override's target) for a pack with thresholds, full for one
// without, and full during the charging legs of a calibration. It is 0 during
// the calibration legs that drain the pack, since nothing in them charges
// toward a mark.
func chargeTarget(hasThresholds bool, bandStop int, calibrating bool, phase string) int {
	switch {
	case calibrating && (phase == calib.PhaseCharging || phase == calib.PhaseRecharging):
		return 100
	case calibrating:
		return 0
	case hasThresholds:
		return bandStop
	default:
		return 100
	}
}

// estimates are the time-to-target figures for one reading. A zero duration
// means no estimate: the pack is not moving in that direction, the power is too
// small to divide by, or the result is past maxEstimate.
type estimates struct {
	toStop  time.Duration // until charging reaches target
	toEmpty time.Duration // until a discharging pack is empty
}

// estimate projects reading r forward at powerUW (signed, positive into the
// pack). The charge estimate is linear to target, which slightly flatters a
// target of 100 (the charger tapers over the last few percent) but is close
// for a band stop of 80, where the taper has not begun. The runtime runs to 0%,
// as Plasma's own battery applet reports it.
func estimate(r battery.Reading, powerUW float64, target int, calibrating bool) estimates {
	var e estimates
	switch {
	case r.Status == battery.StatusCharging && target > 0 && powerUW >= minEstimatePowerUW:
		need := float64(r.EnergyFullUWh)*float64(target)/100 - float64(r.EnergyNowUWh)
		if need > 0 {
			e.toStop = clampEstimate(need / powerUW)
		}
	case r.Status == battery.StatusDischarging && !calibrating && -powerUW >= minEstimatePowerUW:
		e.toEmpty = clampEstimate(float64(r.EnergyNowUWh) / -powerUW)
	}
	return e
}

// clampEstimate turns hours into a duration, or zero past maxEstimate.
func clampEstimate(hours float64) time.Duration {
	d := time.Duration(hours * float64(time.Hour))
	if d > maxEstimate {
		return 0
	}
	return d.Round(time.Second)
}
