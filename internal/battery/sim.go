package battery

import (
	"fmt"
	"sync"
	"time"
)

// Sim is an in-memory Battery for tests. It moves a modelled state of charge in
// response to the behaviour and thresholds written to it, advanced in virtual
// time by Advance, so a test clock can drive the calibration state machine
// deterministically without real hardware or real time.
//
// It is safe for concurrent use: the daemon reads from its run loop while the
// control server may read for a status request.
type Sim struct {
	mu sync.Mutex

	capPct    float64 // state of charge, 0-100, kept as a float for precision
	start     int
	stop      int
	hasThresh bool
	charging  bool // hysteresis latch: true between crossing below start and reaching stop
	behaviour Behaviour
	supported []Behaviour
	acOnline  bool

	energyFullUWh       int64
	energyFullDesignUWh int64
	voltageNowUV        int64
	cycleCount          int

	// Rates in percent of charge per hour of virtual time.
	chargeRate    float64
	dischargeRate float64

	// Relearn model: once the pack is driven full, then down to or below
	// relearnFloor, then full again, the gauge "relearns" and energy_full
	// snaps to design. Mirrors what a real learning cycle does, so a test can
	// assert the daemon records the corrected FCC.
	relearnFloor int
	sawFull      bool
	sawLow       bool
}

// SimConfig seeds a Sim. Zero fields take sensible defaults.
type SimConfig struct {
	Capacity              int
	Start                 int
	Stop                  int
	HasThresholds         bool
	Supported             []Behaviour
	ACOnline              bool
	EnergyFullUWh         int64
	EnergyFullDesignUWh   int64
	VoltageNowUV          int64
	CycleCount            int
	ChargeRatePctPerHr    float64
	DischargeRatePctPerHr float64
	RelearnFloor          int
}

// NewSim builds a Sim from a config, filling defaults for anything unset.
func NewSim(c SimConfig) *Sim {
	if c.Supported == nil {
		c.Supported = []Behaviour{BehaviourAuto, BehaviourInhibitCharge, BehaviourForceDischarge}
	}
	if c.Start == 0 {
		c.Start = 75
	}
	if c.Stop == 0 {
		c.Stop = 80
	}
	if c.ChargeRatePctPerHr == 0 {
		c.ChargeRatePctPerHr = 60
	}
	if c.DischargeRatePctPerHr == 0 {
		c.DischargeRatePctPerHr = 30
	}
	if c.RelearnFloor == 0 {
		c.RelearnFloor = 10
	}
	return &Sim{
		capPct:              float64(c.Capacity),
		start:               c.Start,
		stop:                c.Stop,
		hasThresh:           c.HasThresholds,
		behaviour:           BehaviourAuto,
		supported:           c.Supported,
		acOnline:            c.ACOnline,
		energyFullUWh:       c.EnergyFullUWh,
		energyFullDesignUWh: c.EnergyFullDesignUWh,
		voltageNowUV:        c.VoltageNowUV,
		cycleCount:          c.CycleCount,
		chargeRate:          c.ChargeRatePctPerHr,
		dischargeRate:       c.DischargeRatePctPerHr,
		relearnFloor:        c.RelearnFloor,
	}
}

// SetAC changes the simulated AC line.
func (s *Sim) SetAC(online bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acOnline = online
}

// Advance moves the modelled charge forward by d of virtual time according to
// the current behaviour, thresholds and AC line.
func (s *Sim) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	hrs := d.Hours()
	switch s.behaviour {
	case BehaviourForceDischarge:
		s.capPct -= s.dischargeRate * hrs
	case BehaviourInhibitCharge:
		if !s.acOnline {
			s.capPct -= s.dischargeRate * hrs
		}
	default: // auto
		if s.acOnline {
			s.capPct += s.chargeStepLocked(hrs)
		} else {
			s.charging = false
			s.capPct -= s.dischargeRate * hrs
		}
	}
	if s.capPct > 100 {
		s.capPct = 100
	}
	if s.capPct < 0 {
		s.capPct = 0
	}
	s.updateRelearn()
}

// chargeStepLocked returns the charge to add this step under auto behaviour on
// AC, modelling threshold hysteresis: without thresholds the pack charges to
// 100; with them, charging latches on when charge falls below start and off
// when it reaches stop, so a pack between start and stop that is not already
// charging stays put (as real ThinkPad firmware does).
func (s *Sim) chargeStepLocked(hrs float64) float64 {
	step := s.chargeRate * hrs
	if !s.hasThresh {
		return clampStep(step, s.capPct, 100)
	}
	if s.capPct < float64(s.start) {
		s.charging = true
	}
	if s.capPct >= float64(s.stop) {
		s.charging = false
	}
	if !s.charging {
		return 0
	}
	// Terminate exactly at the stop threshold rather than overshooting when a
	// coarse time step would carry the charge past it.
	return clampStep(step, s.capPct, float64(s.stop))
}

// clampStep limits a positive charge increment so cap+step does not exceed max.
func clampStep(step, cap, max float64) float64 {
	if cap+step > max {
		return max - cap
	}
	return step
}

// updateRelearn tracks the full -> low -> full sequence and snaps the learned
// capacity to design when it completes.
func (s *Sim) updateRelearn() {
	cap := int(s.capPct + 0.5)
	if cap >= 99 {
		if s.sawFull && s.sawLow {
			if s.energyFullDesignUWh > 0 {
				s.energyFullUWh = s.energyFullDesignUWh
			}
			s.sawLow = false
		}
		s.sawFull = true
	}
	if s.sawFull && cap <= s.relearnFloor {
		s.sawLow = true
	}
}

func (s *Sim) Read() (Reading, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cap := int(s.capPct + 0.5)
	return Reading{
		Capacity:            cap,
		Status:              s.statusLocked(cap),
		EnergyNowUWh:        int64(float64(s.energyFullUWh) * s.capPct / 100),
		EnergyFullUWh:       s.energyFullUWh,
		EnergyFullDesignUWh: s.energyFullDesignUWh,
		VoltageNowUV:        s.voltageNowUV,
		PowerNowUW:          s.powerLocked(cap),
		CycleCount:          s.cycleCount,
		ACOnline:            s.acOnline,
	}, nil
}

// powerLocked models instantaneous pack power from the active rate: a rate in
// percent-per-hour times the learned full capacity (uWh) over 100 is microwatts.
// A pack at rest (inhibit-charge on AC, or auto-not-charging on AC) draws zero.
func (s *Sim) powerLocked(cap int) int64 {
	rate := 0.0
	switch s.behaviour {
	case BehaviourForceDischarge:
		rate = s.dischargeRate
	case BehaviourInhibitCharge:
		if !s.acOnline {
			rate = s.dischargeRate
		}
	default: // auto
		switch {
		case !s.acOnline:
			rate = s.dischargeRate
		case s.charging && cap < s.stop:
			rate = s.chargeRate
		}
	}
	return int64(rate / 100 * float64(s.energyFullUWh))
}

func (s *Sim) statusLocked(cap int) Status {
	switch s.behaviour {
	case BehaviourForceDischarge:
		return StatusDischarging
	case BehaviourInhibitCharge:
		if s.acOnline {
			return StatusNotCharging
		}
		return StatusDischarging
	default:
		if !s.acOnline {
			return StatusDischarging
		}
		if cap >= 100 {
			return StatusFull
		}
		if !s.hasThresh {
			return StatusCharging
		}
		if s.charging {
			return StatusCharging
		}
		return StatusNotCharging
	}
}

func (s *Sim) HasThresholds() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hasThresh
}

func (s *Sim) Thresholds() (start, stop int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasThresh {
		return 0, 0, fmt.Errorf("no charge thresholds on this machine")
	}
	return s.start, s.stop, nil
}

func (s *Sim) SetThresholds(start, stop int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasThresh {
		return fmt.Errorf("no charge thresholds on this machine")
	}
	if start > stop {
		return fmt.Errorf("start %d above stop %d", start, stop)
	}
	s.start, s.stop = start, stop
	return nil
}

func (s *Sim) Supported() ([]Behaviour, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Behaviour, len(s.supported))
	copy(out, s.supported)
	return out, nil
}

func (s *Sim) Behaviour() (Behaviour, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.behaviour, nil
}

func (s *Sim) SetBehaviour(b Behaviour) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !Supports(s.supported, b) {
		return fmt.Errorf("behaviour %q not supported", b)
	}
	s.behaviour = b
	return nil
}
