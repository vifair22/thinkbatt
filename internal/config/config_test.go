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
[thinkbatt]
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

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"not key=value":  "band_start 70",
		"unknown key":    "nope = 1",
		"bad int":        "band_start = seventy",
		"bad bool":       "calib_enabled = maybe",
		"start >= stop":  "band_start = 80\nband_stop = 80",
		"floor too low":  "calib_floor = 2",
		"floor >= start": "calib_floor = 75",
		"start too low":  "band_start = 0",
		"stop too high":  "band_stop = 101",
		"cycles too low": "calib_every_cycles = 0",
		"days too low":   "calib_every_days = 0",
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
