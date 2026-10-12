package battery

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTree lays out a minimal power_supply tree: one battery and one Mains
// supply, returning the root.
func writeTree(t *testing.T, attrs map[string]string, acOnline string) string {
	t.Helper()
	root := t.TempDir()
	bat := filepath.Join(root, "BAT0")
	if err := os.MkdirAll(bat, 0o755); err != nil {
		t.Fatal(err)
	}
	for k, v := range attrs {
		if err := os.WriteFile(filepath.Join(bat, k), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ac := filepath.Join(root, "AC0")
	if err := os.MkdirAll(ac, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ac, "type"), []byte("Mains\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ac, "online"), []byte(acOnline+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func baseAttrs() map[string]string {
	return map[string]string{
		"capacity":                       "79",
		"status":                         "Not charging",
		"energy_now":                     "88660000",
		"energy_full":                    "112700000",
		"energy_full_design":             "94000000",
		"voltage_now":                    "12173000",
		"cycle_count":                    "4",
		"charge_control_start_threshold": "75",
		"charge_control_end_threshold":   "80",
		"charge_behaviour":               "[auto] inhibit-charge force-discharge",
	}
}

func TestSysfsRead(t *testing.T) {
	root := writeTree(t, baseAttrs(), "1")
	s := NewSysfs(root, "BAT0")
	r, err := s.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if r.Capacity != 79 || r.Status != StatusNotCharging || r.CycleCount != 4 {
		t.Errorf("unexpected reading: %+v", r)
	}
	if r.EnergyFullUWh != 112700000 || r.EnergyFullDesignUWh != 94000000 {
		t.Errorf("energy fields wrong: %+v", r)
	}
	if !r.ACOnline {
		t.Errorf("AC should be online")
	}
}

func TestSysfsReadMissingOptional(t *testing.T) {
	attrs := map[string]string{"capacity": "50", "status": "Discharging"}
	root := writeTree(t, attrs, "0")
	s := NewSysfs(root, "BAT0")
	r, err := s.Read()
	if err != nil {
		t.Fatalf("Read with only required attrs: %v", err)
	}
	if r.Capacity != 50 || r.EnergyFullUWh != 0 || r.ACOnline {
		t.Errorf("unexpected: %+v", r)
	}
}

func TestSysfsThresholds(t *testing.T) {
	root := writeTree(t, baseAttrs(), "1")
	s := NewSysfs(root, "BAT0")
	if !s.HasThresholds() {
		t.Fatal("HasThresholds should be true")
	}
	if err := s.SetThresholds(60, 70); err != nil {
		t.Fatalf("SetThresholds: %v", err)
	}
	start, stop, err := s.Thresholds()
	if err != nil {
		t.Fatal(err)
	}
	if start != 60 || stop != 70 {
		t.Errorf("thresholds = %d/%d, want 60/70", start, stop)
	}
}

func TestSysfsBehaviour(t *testing.T) {
	root := writeTree(t, baseAttrs(), "1")
	s := NewSysfs(root, "BAT0")
	sup, err := s.Supported()
	if err != nil {
		t.Fatal(err)
	}
	if !Supports(sup, BehaviourForceDischarge) || !Supports(sup, BehaviourInhibitCharge) {
		t.Errorf("supported = %v, want force-discharge and inhibit-charge", sup)
	}
	active, err := s.Behaviour()
	if err != nil {
		t.Fatal(err)
	}
	if active != BehaviourAuto {
		t.Errorf("active = %q, want auto", active)
	}
	if err := s.SetBehaviour(BehaviourForceDischarge); err != nil {
		t.Fatalf("SetBehaviour: %v", err)
	}
	if got, _ := readStr(filepath.Join(root, "BAT0", "charge_behaviour")); got != "force-discharge" {
		t.Errorf("written behaviour = %q", got)
	}
}

func TestSysfsNoBehaviourAttr(t *testing.T) {
	attrs := baseAttrs()
	delete(attrs, "charge_behaviour")
	root := writeTree(t, attrs, "1")
	s := NewSysfs(root, "BAT0")
	sup, err := s.Supported()
	if err != nil {
		t.Fatalf("Supported with no attr: %v", err)
	}
	if len(sup) != 1 || sup[0] != BehaviourAuto {
		t.Errorf("no charge_behaviour should report only auto, got %v", sup)
	}
}

func TestSysfsNoThresholds(t *testing.T) {
	attrs := baseAttrs()
	delete(attrs, "charge_control_start_threshold")
	delete(attrs, "charge_control_end_threshold")
	root := writeTree(t, attrs, "1")
	s := NewSysfs(root, "BAT0")
	if s.HasThresholds() {
		t.Errorf("HasThresholds should be false without the attr")
	}
}

func TestNewSysfsDefaults(t *testing.T) {
	s := NewSysfs("", "")
	if s.root != DefaultRoot || s.bat != "BAT0" {
		t.Errorf("defaults not applied: root=%q bat=%q", s.root, s.bat)
	}
}
