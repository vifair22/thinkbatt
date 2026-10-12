// Package caps detects, at runtime, what charge control a given ThinkPad
// actually offers. thinkbatt targets several models whose firmware differs: a
// coreboot machine exposes force-discharge and inhibit-charge, a stock-firmware
// one may expose only the charge thresholds, or nothing. Features are gated on
// the detected profile rather than on the model, so the same binary runs
// everywhere and simply does less where the hardware offers less.
package caps

import (
	"fmt"

	"git.airies.net/vifair22/thinkbatt/internal/battery"
)

// Profile is what a machine can do with its battery charge control.
type Profile struct {
	HasThresholds     bool
	CanInhibit        bool
	CanForceDischarge bool
	Supported         []battery.Behaviour
}

// Detect probes the battery for its capabilities.
func Detect(b battery.Battery) (Profile, error) {
	supported, err := b.Supported()
	if err != nil {
		return Profile{}, fmt.Errorf("probe charge_behaviour: %w", err)
	}
	return Profile{
		HasThresholds:     b.HasThresholds(),
		CanInhibit:        battery.Supports(supported, battery.BehaviourInhibitCharge),
		CanForceDischarge: battery.Supports(supported, battery.BehaviourForceDischarge),
		Supported:         supported,
	}, nil
}

// CanManageBand reports whether the resting charge band can be enforced.
func (p Profile) CanManageBand() bool { return p.HasThresholds }

// CanCalibrate reports whether a full fuel-gauge learning cycle is possible: it
// needs both a way to drive the pack full (thresholds) and a way to actively
// discharge it on AC (force-discharge).
func (p Profile) CanCalibrate() bool { return p.HasThresholds && p.CanForceDischarge }

// Summary is a one-line human description for logs and the status command.
func (p Profile) Summary() string {
	band, cal := "no", "no"
	if p.CanManageBand() {
		band = "yes"
	}
	if p.CanCalibrate() {
		cal = "yes"
	}
	return fmt.Sprintf("thresholds=%s force-discharge=%t inhibit-charge=%t band=%s calibrate=%s",
		yn(p.HasThresholds), p.CanForceDischarge, p.CanInhibit, band, cal)
}

func yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
