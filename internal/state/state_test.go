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
	if s.Last("BAT0") != nil {
		t.Errorf("missing file should be empty state")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	var want State
	want.SetLast("BAT0", &Calibration{
		StartedAt:           time.Date(2026, 10, 11, 20, 0, 0, 0, time.UTC),
		FinishedAt:          time.Date(2026, 10, 11, 23, 0, 0, 0, time.UTC),
		Outcome:             OutcomeOK,
		Reason:              "learning cycle complete",
		CycleCountAt:        5,
		EnergyFullBeforeUWh: 112_700_000,
		EnergyFullAfterUWh:  94_000_000,
	})
	want.SetLast("BAT1", &Calibration{Outcome: OutcomeAborted, CycleCountAt: 2})
	if err := Save(p, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b0 := got.Last("BAT0")
	if b0 == nil || b0.Outcome != OutcomeOK || b0.EnergyFullAfterUWh != 94_000_000 ||
		!b0.FinishedAt.Equal(want.Last("BAT0").FinishedAt) {
		t.Errorf("BAT0 round trip mismatch: %+v", b0)
	}
	if b1 := got.Last("BAT1"); b1 == nil || b1.Outcome != OutcomeAborted {
		t.Errorf("BAT1 round trip mismatch: %+v", b1)
	}
}

func TestSaveOverwrites(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	var s1 State
	s1.SetLast("BAT0", &Calibration{CycleCountAt: 1})
	if err := Save(p, s1); err != nil {
		t.Fatal(err)
	}
	var s2 State
	s2.SetLast("BAT0", &Calibration{CycleCountAt: 2})
	if err := Save(p, s2); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Last("BAT0").CycleCountAt != 2 {
		t.Errorf("overwrite failed: CycleCountAt = %d, want 2", got.Last("BAT0").CycleCountAt)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(p))
	for _, e := range entries {
		if e.Name() != "state.json" {
			t.Errorf("stray file left: %s", e.Name())
		}
	}
}

func TestMigrateLegacySinglePack(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	// A state.json written by the pre-0.2 single-pack daemon.
	legacy := `{"last_calibration":{"outcome":"ok","cycle_count_at":5,"energy_full_after_uwh":94000000}}`
	if err := os.WriteFile(p, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	b0 := got.Last("BAT0")
	if b0 == nil || b0.Outcome != OutcomeOK || b0.CycleCountAt != 5 {
		t.Errorf("legacy record not migrated onto BAT0: %+v", b0)
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
