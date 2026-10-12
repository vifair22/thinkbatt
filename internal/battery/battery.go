// Package battery is the hardware surface thinkbatt sits on: a small interface
// over the kernel power_supply sysfs (the battery's charge thresholds, its
// charge behaviour, and a reading of its state), plus the AC line. Everything
// above this package talks to the interface, so the daemon runs against real
// sysfs and the tests run against an in-memory simulator.
package battery

// Status is the kernel's power_supply "status" value for the pack.
type Status string

const (
	StatusCharging    Status = "Charging"
	StatusDischarging Status = "Discharging"
	StatusNotCharging Status = "Not charging"
	StatusFull        Status = "Full"
	StatusUnknown     Status = "Unknown"
)

// Behaviour is a charge_behaviour token. Auto is the normal firmware policy;
// InhibitCharge holds charge off; ForceDischarge actively drains the pack even
// on AC. Which tokens a machine supports is a runtime property (see Supported).
type Behaviour string

const (
	BehaviourAuto           Behaviour = "auto"
	BehaviourInhibitCharge  Behaviour = "inhibit-charge"
	BehaviourForceDischarge Behaviour = "force-discharge"
)

// Reading is a single snapshot of the pack and the AC line. Energy fields are
// in microwatt-hours and voltage in microvolts, as the kernel reports them;
// callers that want watt-hours divide by 1e6.
type Reading struct {
	Capacity            int // state of charge, percent 0-100
	Status              Status
	EnergyNowUWh        int64
	EnergyFullUWh       int64 // the gauge's learned full-charge capacity (FCC)
	EnergyFullDesignUWh int64
	VoltageNowUV        int64
	CycleCount          int
	ACOnline            bool
}

// Battery is the full hardware surface. A real implementation writes sysfs; the
// simulator moves an in-memory model. All methods may return an error; callers
// treat an error as "do not act" rather than guessing.
type Battery interface {
	// Read returns a current snapshot.
	Read() (Reading, error)

	// HasThresholds reports whether the charge_control_{start,end}_threshold
	// attributes exist on this machine.
	HasThresholds() bool
	// Thresholds returns the current start and stop percentages.
	Thresholds() (start, stop int, err error)
	// SetThresholds writes stop first, then start, so start is never briefly
	// above stop whatever the previous values were.
	SetThresholds(start, stop int) error

	// Supported returns the charge_behaviour tokens this machine offers. A
	// machine with no charge_behaviour attribute returns only BehaviourAuto.
	Supported() ([]Behaviour, error)
	// Behaviour returns the currently active charge_behaviour.
	Behaviour() (Behaviour, error)
	// SetBehaviour writes a charge_behaviour token.
	SetBehaviour(Behaviour) error
}

// Supports reports whether b is in the supported set.
func Supports(set []Behaviour, b Behaviour) bool {
	for _, s := range set {
		if s == b {
			return true
		}
	}
	return false
}
