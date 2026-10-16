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

func TestSysfsSetThresholdsOrder(t *testing.T) {
	// The kernel rejects a write that would make start exceed end. A plain-file
	// fake cannot reproduce that rejection, so the write ORDER is asserted via
	// startBeforeEnd; this case guards that SetThresholds reads the current end
	// and lands the right final values when lowering a high band to a low one.
	attrs := baseAttrs()
	attrs["charge_control_start_threshold"] = "99"
	attrs["charge_control_end_threshold"] = "100"
	root := writeTree(t, attrs, "1")
	s := NewSysfs(root, "BAT0")
	if err := s.SetThresholds(75, 80); err != nil {
		t.Fatalf("SetThresholds lowering from 99/100: %v", err)
	}
	start, stop, err := s.Thresholds()
	if err != nil {
		t.Fatal(err)
	}
	if start != 75 || stop != 80 {
		t.Errorf("thresholds = %d/%d, want 75/80", start, stop)
	}
}

func TestStartBeforeEnd(t *testing.T) {
	tests := []struct {
		name     string
		curEnd   int
		newStart int
		want     bool
	}{
		{"lower band writes start first", 100, 75, true},
		{"raise band writes end first", 80, 99, false},
		{"equal to current end writes start first", 80, 80, true},
		{"well under current end writes start first", 80, 10, true},
		{"just above current end writes end first", 80, 81, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := startBeforeEnd(tc.curEnd, tc.newStart); got != tc.want {
				t.Errorf("startBeforeEnd(%d, %d) = %v, want %v", tc.curEnd, tc.newStart, got, tc.want)
			}
		})
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

func TestSysfsPowerDerivedFromCurrent(t *testing.T) {
	attrs := baseAttrs()
	attrs["voltage_now"] = "11273000"
	attrs["current_now"] = "-601000" // signed by some drivers; taken as a magnitude
	r, err := NewSysfs(writeTree(t, attrs, "0"), "BAT0").Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := int64(601000) * 11273000 / 1_000_000; r.PowerNowUW != want {
		t.Errorf("derived power = %d uW, want %d", r.PowerNowUW, want)
	}

	attrs["power_now"] = "-7000000" // reported directly: preferred, as a magnitude
	r, err = NewSysfs(writeTree(t, attrs, "0"), "BAT0").Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if r.PowerNowUW != 7_000_000 {
		t.Errorf("power_now = %d uW, want 7000000", r.PowerNowUW)
	}
}

// c14Attrs is a pack whose firmware reports charge units: no energy_* or
// power_now, as the C14's ChromeEC ACPI battery does.
func c14Attrs() map[string]string {
	return map[string]string{
		"capacity":           "58",
		"status":             "Discharging",
		"charge_now":         "2898000",
		"charge_full":        "4928000",
		"charge_full_design": "4948000",
		"current_now":        "629000",
		"voltage_now":        "11273000",
		"voltage_min_design": "11520000",
		"cycle_count":        "12",
	}
}

func TestSysfsChargeUnitsConvertToEnergy(t *testing.T) {
	r, err := NewSysfs(writeTree(t, c14Attrs(), "0"), "BAT0").Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	// uAh * uV(min design) / 1e6 = uWh
	if want := int64(4948000) * 11520000 / 1_000_000; r.EnergyFullDesignUWh != want {
		t.Errorf("design energy = %d uWh, want %d", r.EnergyFullDesignUWh, want)
	}
	if want := int64(4928000) * 11520000 / 1_000_000; r.EnergyFullUWh != want {
		t.Errorf("full energy = %d uWh, want %d", r.EnergyFullUWh, want)
	}
	if want := int64(2898000) * 11520000 / 1_000_000; r.EnergyNowUWh != want {
		t.Errorf("energy now = %d uWh, want %d", r.EnergyNowUWh, want)
	}
	if want := int64(629000) * 11273000 / 1_000_000; r.PowerNowUW != want {
		t.Errorf("power = %d uW, want %d", r.PowerNowUW, want)
	}
}

func TestSysfsChargeUnitsFallBackToVoltageNow(t *testing.T) {
	attrs := c14Attrs()
	delete(attrs, "voltage_min_design")
	r, err := NewSysfs(writeTree(t, attrs, "0"), "BAT0").Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if want := int64(4948000) * 11273000 / 1_000_000; r.EnergyFullDesignUWh != want {
		t.Errorf("design energy = %d uWh, want %d (via voltage_now)", r.EnergyFullDesignUWh, want)
	}
}

func TestSysfsChargeUnitsReadError(t *testing.T) {
	for _, attr := range []string{"voltage_min_design", "charge_full"} {
		attrs := c14Attrs()
		root := writeTree(t, attrs, "0")
		p := filepath.Join(root, "BAT0", attr)
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p, 0o755); err != nil { // a directory reads as an error, not as absent
			t.Fatal(err)
		}
		if _, err := NewSysfs(root, "BAT0").Read(); err == nil {
			t.Errorf("Read with unreadable %s should error", attr)
		}
	}
}
