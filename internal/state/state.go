// Package state persists what the daemon must remember across restarts: when
// calibration last ran, the cycle count then, and the learned capacity before
// and after. It is written atomically (temp file plus rename) so a crash during
// a write never leaves a truncated file. A missing file is an empty state, not
// an error, which is the first-boot case.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Outcome records how a calibration run ended.
type Outcome string

const (
	OutcomeOK      Outcome = "ok"
	OutcomeAborted Outcome = "aborted"
	OutcomeFailed  Outcome = "failed"
)

// Calibration is the record of one calibration run.
type Calibration struct {
	StartedAt           time.Time `json:"started_at"`
	FinishedAt          time.Time `json:"finished_at"`
	Outcome             Outcome   `json:"outcome"`
	Reason              string    `json:"reason,omitempty"`
	CycleCountAt        int       `json:"cycle_count_at"`
	EnergyFullBeforeUWh int64     `json:"energy_full_before_uwh"`
	EnergyFullAfterUWh  int64     `json:"energy_full_after_uwh"`
}

// State is the whole persisted record.
type State struct {
	LastCalibration *Calibration `json:"last_calibration,omitempty"`
}

// Load reads the state file. A nonexistent file returns a zero State.
func Load(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, fmt.Errorf("read state %s: %w", path, err)
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return State{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	return s, nil
}

// Save writes the state atomically: a temp file in the same directory, fsynced,
// then renamed over the target.
func Save(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp state in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename has consumed it

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename state into place: %w", err)
	}
	return nil
}
