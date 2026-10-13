package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDefaultsAndOverrides(t *testing.T) {
	in := `
# a comment
; another comment
[global]
band_start = 70
band_stop = 85
calib_floor = 10
calib_enabled = false
`
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.BandStart != 70 || c.BandStop != 85 || c.CalibFloor != 10 {
		t.Errorf("overrides not applied: %+v", c)
	}
	if c.CalibEnabled {
		t.Errorf("calib_enabled should be false")
	}
	// Untouched keys keep their defaults.
	if c.CalibEveryCycles != Default().CalibEveryCycles {
		t.Errorf("calib_every_cycles = %d, want default %d", c.CalibEveryCycles, Default().CalibEveryCycles)
	}
}

func TestPerPackOverrides(t *testing.T) {
	in := `
band_start = 75
band_stop = 80

[BAT1]
band_start = 50
band_stop = 60
calib_enabled = false
`
	f, err := ParseFile(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	// A pack with no section is the global policy.
	bat0, err := f.For("BAT0")
	if err != nil {
		t.Fatalf("For(BAT0): %v", err)
	}
	if bat0 != f.Global || bat0.BandStart != 75 || bat0.BandStop != 80 {
		t.Errorf("BAT0 should equal global policy, got %+v", bat0)
	}
	// The overridden pack carries its own band and disabled calibration, while
	// untouched keys still fall through to the global defaults.
	bat1, err := f.For("BAT1")
	if err != nil {
		t.Fatalf("For(BAT1): %v", err)
	}
	if bat1.BandStart != 50 || bat1.BandStop != 60 {
		t.Errorf("BAT1 band = %d/%d, want 50/60", bat1.BandStart, bat1.BandStop)
	}
	if bat1.CalibEnabled {
		t.Errorf("BAT1 calib_enabled should be false")
	}
	if bat1.CalibFloor != f.Global.CalibFloor {
		t.Errorf("BAT1 calib_floor = %d, want global %d", bat1.CalibFloor, f.Global.CalibFloor)
	}
	if got := f.OverriddenPacks(); len(got) != 1 || got[0] != "BAT1" {
		t.Errorf("OverriddenPacks = %v, want [BAT1]", got)
	}
}

func TestPerPackSectionErrors(t *testing.T) {
	cases := map[string]string{
		"socket_group in section": "[BAT1]\nsocket_group = wheel",
		"bad override value":      "[BAT1]\nband_start = seventy",
		"override fails validate": "[BAT1]\nband_start = 90\nband_stop = 85",
		"empty section header":    "[]\nband_start = 70",
		"unknown key in section":  "[BAT1]\nnope = 1",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFile(strings.NewReader(in)); err == nil {
				t.Errorf("expected error for %q", in)
			}
		})
	}
}

func TestParseCalibrationTuning(t *testing.T) {
	in := `
rest_at_full_minutes = 90
rest_at_floor_minutes = 0
discharge_c_rate = 0.25
charge_timeout_hours = 6
discharge_timeout_hours = 10
`
	c, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.RestAtFullMinutes != 90 || c.RestAtFloorMinutes != 0 {
		t.Errorf("rest minutes = %d/%d, want 90/0", c.RestAtFullMinutes, c.RestAtFloorMinutes)
	}
	if c.DischargeCRate != 0.25 {
		t.Errorf("discharge_c_rate = %.3f, want 0.25", c.DischargeCRate)
	}
	if c.ChargeTimeoutHours != 6 || c.DischargeTimeoutHours != 10 {
		t.Errorf("timeouts = %d/%d h, want 6/10", c.ChargeTimeoutHours, c.DischargeTimeoutHours)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"not key=value":    "band_start 70",
		"unknown key":      "nope = 1",
		"bad int":          "band_start = seventy",
		"bad bool":         "calib_enabled = maybe",
		"start >= stop":    "band_start = 80\nband_stop = 80",
		"floor too low":    "calib_floor = 2",
		"floor >= start":   "calib_floor = 75",
		"start too low":    "band_start = 0",
		"stop too high":    "band_stop = 101",
		"cycles too low":   "calib_every_cycles = 0",
		"days too low":     "calib_every_days = 0",
		"bad float":        "discharge_c_rate = fast",
		"rest full high":   "rest_at_full_minutes = 1000",
		"rest floor neg":   "rest_at_floor_minutes = -1",
		"c-rate negative":  "discharge_c_rate = -0.1",
		"c-rate too high":  "discharge_c_rate = 3",
		"charge timeout 0": "charge_timeout_hours = 0",
		"disch timeout 0":  "discharge_timeout_hours = 0",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(in)); err == nil {
				t.Errorf("expected error for %q", in)
			}
		})
	}
}

func TestLoadMissingFileIsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "absent.ini"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if c != Default() {
		t.Errorf("missing file = %+v, want defaults", c)
	}
}

func TestLoadFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "thinkbatt.ini")
	if err := os.WriteFile(p, []byte("band_stop = 90\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.BandStop != 90 {
		t.Errorf("BandStop = %d, want 90", c.BandStop)
	}
}

func TestDefaultValidates(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Errorf("default config must validate: %v", err)
	}
}
