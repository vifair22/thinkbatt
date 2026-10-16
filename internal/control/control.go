// Package control is the thin request/response protocol between the daemon and
// the thinkbatt CLI, spoken over a unix-domain socket. One JSON request and one
// JSON response per connection, newline-delimited. The daemon implements
// Handler; the CLI uses Client.
package control

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/state"
)

// Command names.
const (
	CmdStatus      = "status"
	CmdCalibrate   = "calibrate"
	CmdCancel      = "cancel"
	CmdBand        = "band"
	CmdCharge      = "charge"
	CmdChargeReset = "charge-reset"
)

// UntilFull is the "until" argument of a charge request that ends once the pack
// is full rather than at a deadline. Any other value is an RFC 3339 deadline.
const UntilFull = "full"

// Request is a single command from the CLI.
type Request struct {
	Cmd  string            `json:"cmd"`
	Args map[string]string `json:"args,omitempty"`
}

// Response is the daemon's reply. Data carries a command-specific payload.
type Response struct {
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// PackStatus is the state of one battery pack.
type PackStatus struct {
	Name            string `json:"name"`
	Capacity        int    `json:"capacity"`
	Status          string `json:"status"`
	BehaviourActive string `json:"behaviour_active"`
	BandStart       int    `json:"band_start"`
	BandStop        int    `json:"band_stop"`
	HasThresholds   bool   `json:"has_thresholds"`
	// PowerUW is the power into the pack, microwatts: positive while charging,
	// negative while discharging. VoltageUV is the pack voltage, microvolts.
	PowerUW             int64              `json:"power_uw"`
	VoltageUV           int64              `json:"voltage_uv"`
	EnergyFullUWh       int64              `json:"energy_full_uwh"`
	EnergyFullDesignUWh int64              `json:"energy_full_design_uwh"`
	CapsSummary         string             `json:"caps_summary"`
	CalibrationRunning  bool               `json:"calibration_running"`
	CalibrationPhase    string             `json:"calibration_phase,omitempty"`
	LastCalibration     *state.Calibration `json:"last_calibration,omitempty"`
	// Override is the active charge override, if any. While it is set the pack
	// is held at its target instead of BandStart/BandStop, which stay the
	// resting band it returns to.
	Override *state.Override `json:"override,omitempty"`
}

// StatusInfo is the payload of a status response: one entry per present pack.
type StatusInfo struct {
	Packs []PackStatus `json:"packs"`
}

// Handler is the daemon-side behaviour behind the socket. The battery argument
// names a pack; an empty name means "the only pack" for calibrate (an error
// when several are present) and "every band-capable pack" for band.
type Handler interface {
	Status() (StatusInfo, error)
	// Calibrate requests a calibration run on a pack; now bypasses the due-check.
	Calibrate(battery string, now bool) error
	Cancel() error
	SetBand(battery string, start, stop int) error
	// SetOverride holds a pack (or every band-capable pack) at target until the
	// deadline, or until it is full when untilFull is set. It cancels a
	// calibration running on an affected pack.
	SetOverride(battery string, target int, until time.Time, untilFull bool) error
	// ClearOverride ends a pack's override (or every override) early.
	ClearOverride(battery string) error
}

// Client dials the daemon socket for one command.
type Client struct {
	Path    string
	Timeout time.Duration
}

// Do sends req and returns the response.
func (c Client) Do(req Request) (Response, error) {
	to := c.Timeout
	if to <= 0 {
		to = 5 * time.Second
	}
	conn, err := net.DialTimeout("unix", c.Path, to)
	if err != nil {
		return Response{}, fmt.Errorf("connect to daemon at %s: %w", c.Path, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(to))

	enc, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("encode request: %w", err)
	}
	if _, err := conn.Write(append(enc, '\n')); err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, fmt.Errorf("decode response: %w", err)
	}
	return resp, nil
}
