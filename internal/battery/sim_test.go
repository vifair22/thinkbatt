package battery

import (
	"testing"
	"time"
)

func TestSimChargeHysteresis(t *testing.T) {
	// On AC in a 75/80 band, a pack sitting at 79 does not charge (above start).
	s := NewSim(SimConfig{Capacity: 79, HasThresholds: true, Start: 75, Stop: 80, ACOnline: true})
	s.Advance(time.Hour)
	if r, _ := s.Read(); r.Capacity != 79 {
		t.Errorf("idle-in-band pack charged to %d, want 79", r.Capacity)
	}
	// Drop it below start; now it charges up to stop and stops.
	s2 := NewSim(SimConfig{Capacity: 60, HasThresholds: true, Start: 75, Stop: 80, ACOnline: true})
	for i := 0; i < 20; i++ {
		s2.Advance(time.Hour)
	}
	if r, _ := s2.Read(); r.Capacity != 80 {
		t.Errorf("charged to %d, want stop 80", r.Capacity)
	}
}

func TestSimForceDischargeOnAC(t *testing.T) {
	s := NewSim(SimConfig{Capacity: 100, HasThresholds: true, ACOnline: true})
	if err := s.SetBehaviour(BehaviourForceDischarge); err != nil {
		t.Fatal(err)
	}
	s.Advance(time.Hour)
	r, _ := s.Read()
	if r.Status != StatusDischarging {
		t.Errorf("status = %q, want Discharging", r.Status)
	}
	if r.Capacity >= 100 {
		t.Errorf("force-discharge did not drain: %d", r.Capacity)
	}
}

func TestSimChargeToFull(t *testing.T) {
	// start=99/stop=100 from below drives to 100 and reports Full.
	s := NewSim(SimConfig{Capacity: 80, HasThresholds: true, Start: 99, Stop: 100, ACOnline: true})
	for i := 0; i < 10; i++ {
		s.Advance(time.Hour)
	}
	r, _ := s.Read()
	if r.Capacity != 100 || r.Status != StatusFull {
		t.Errorf("got %d/%q, want 100/Full", r.Capacity, r.Status)
	}
}

func TestSimDischargeOffAC(t *testing.T) {
	s := NewSim(SimConfig{Capacity: 50, HasThresholds: true, ACOnline: false})
	s.Advance(time.Hour)
	if r, _ := s.Read(); r.Capacity >= 50 || r.Status != StatusDischarging {
		t.Errorf("off-AC pack should discharge: %+v", r)
	}
}

func TestSimRelearn(t *testing.T) {
	s := NewSim(SimConfig{
		Capacity: 80, HasThresholds: true, Start: 99, Stop: 100, ACOnline: true,
		EnergyFullUWh: 112_700_000, EnergyFullDesignUWh: 94_000_000, RelearnFloor: 7,
	})
	drive := func(target int, behaviour Behaviour) {
		_ = s.SetBehaviour(behaviour)
		for i := 0; i < 400; i++ {
			r, _ := s.Read()
			if (behaviour == BehaviourForceDischarge && r.Capacity <= target) ||
				(behaviour == BehaviourAuto && r.Capacity >= target) {
				return
			}
			s.Advance(time.Minute)
		}
	}
	drive(100, BehaviourAuto)         // full
	drive(7, BehaviourForceDischarge) // empty
	drive(100, BehaviourAuto)         // full again -> relearn
	if r, _ := s.Read(); r.EnergyFullUWh != 94_000_000 {
		t.Errorf("relearn did not snap FCC to design: got %d", r.EnergyFullUWh)
	}
}

func TestSimSetThresholdsValidation(t *testing.T) {
	s := NewSim(SimConfig{Capacity: 50, HasThresholds: true})
	if err := s.SetThresholds(90, 80); err == nil {
		t.Errorf("start above stop should error")
	}
	noThresh := NewSim(SimConfig{Capacity: 50, HasThresholds: false})
	if err := noThresh.SetThresholds(75, 80); err == nil {
		t.Errorf("setting thresholds with none should error")
	}
	if _, _, err := noThresh.Thresholds(); err == nil {
		t.Errorf("reading thresholds with none should error")
	}
}

func TestSimSetBehaviourUnsupported(t *testing.T) {
	s := NewSim(SimConfig{Capacity: 50, Supported: []Behaviour{BehaviourAuto}})
	if err := s.SetBehaviour(BehaviourForceDischarge); err == nil {
		t.Errorf("unsupported behaviour should error")
	}
}

func TestSimInhibitChargeOnAC(t *testing.T) {
	s := NewSim(SimConfig{Capacity: 60, HasThresholds: true, Start: 75, Stop: 80, ACOnline: true})
	_ = s.SetBehaviour(BehaviourInhibitCharge)
	s.Advance(time.Hour)
	if r, _ := s.Read(); r.Capacity != 60 || r.Status != StatusNotCharging {
		t.Errorf("inhibit-charge on AC should hold charge: %+v", r)
	}
}
