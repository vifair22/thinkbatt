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

// State is the whole persisted record. Calibration is tracked per pack, keyed
// by the kernel battery name (BAT0, BAT1, ...), so a machine with more than one
// pack remembers each one's last run independently.
type State struct {
	LastCalibrations map[string]*Calibration `json:"last_calibrations,omitempty"`
}

// Last returns the last calibration recorded for a pack, or nil if it has never
// calibrated (or the pack is unknown).
func (s State) Last(pack string) *Calibration {
	return s.LastCalibrations[pack]
}

// SetLast records a pack's latest calibration, allocating the map on first use.
func (s *State) SetLast(pack string, c *Calibration) {
	if s.LastCalibrations == nil {
		s.LastCalibrations = map[string]*Calibration{}
	}
	s.LastCalibrations[pack] = c
}

// Load reads the state file. A nonexistent file returns an empty State. A file
// written by the single-pack daemon (a bare "last_calibration") is migrated:
// its record becomes BAT0's, since that was the only pack the old daemon knew.
func Load(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{LastCalibrations: map[string]*Calibration{}}, nil
		}
		return State{}, fmt.Errorf("read state %s: %w", path, err)
	}
	var raw struct {
		LastCalibrations map[string]*Calibration `json:"last_calibrations"`
		LastCalibration  *Calibration            `json:"last_calibration"` // pre-0.2 single-pack
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return State{}, fmt.Errorf("parse state %s: %w", path, err)
	}
	s := State{LastCalibrations: raw.LastCalibrations}
	if s.LastCalibrations == nil {
		s.LastCalibrations = map[string]*Calibration{}
	}
	if raw.LastCalibration != nil {
		if _, ok := s.LastCalibrations["BAT0"]; !ok {
			s.LastCalibrations["BAT0"] = raw.LastCalibration
		}
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
