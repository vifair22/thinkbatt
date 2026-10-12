package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingIsEmpty(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if s.LastCalibration != nil {
		t.Errorf("missing file should be empty state")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	want := State{LastCalibration: &Calibration{
		StartedAt:           time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC),
		FinishedAt:          time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC),
		Outcome:             OutcomeOK,
		Reason:              "learning cycle complete",
		CycleCountAt:        5,
		EnergyFullBeforeUWh: 112_700_000,
		EnergyFullAfterUWh:  94_000_000,
	}}
	if err := Save(p, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.LastCalibration == nil || got.LastCalibration.Outcome != OutcomeOK ||
		got.LastCalibration.EnergyFullAfterUWh != 94_000_000 ||
		!got.LastCalibration.FinishedAt.Equal(want.LastCalibration.FinishedAt) {
		t.Errorf("round trip mismatch: %+v", got.LastCalibration)
	}
}

func TestSaveOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := Save(p, State{LastCalibration: &Calibration{CycleCountAt: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, State{LastCalibration: &Calibration{CycleCountAt: 2}}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastCalibration.CycleCountAt != 2 {
		t.Errorf("overwrite failed: CycleCountAt = %d, want 2", got.LastCalibration.CycleCountAt)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("stray file left: %s", e.Name())
		}
	}
}

func TestLoadReadError(t *testing.T) {
	// A directory where a file is expected makes ReadFile fail with a non
	// not-exist error, exercising that branch.
	dir := filepath.Join(t.TempDir(), "state.json")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Errorf("Load of a directory should error")
	}
}

func TestSaveUnwritableDir(t *testing.T) {
	if err := Save(filepath.Join(t.TempDir(), "no-such-subdir", "state.json"), State{}); err == nil {
		t.Errorf("Save into a nonexistent directory should error")
	}
}

func TestLoadCorrupt(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Errorf("expected error on corrupt state")
	}
}
