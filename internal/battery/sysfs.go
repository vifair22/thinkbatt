package battery

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Sysfs is the real Battery, backed by the kernel power_supply tree. root is
// normally /sys/class/power_supply and bat the pack's name (e.g. BAT0); both
// are injectable so tests can point at a temp directory.
type Sysfs struct {
	root string
	bat  string
}

// DefaultRoot is the kernel power_supply directory.
const DefaultRoot = "/sys/class/power_supply"

// NewSysfs returns a Sysfs. An empty root or bat falls back to the defaults
// (DefaultRoot and "BAT0").
func NewSysfs(root, bat string) *Sysfs {
	if root == "" {
		root = DefaultRoot
	}
	if bat == "" {
		bat = "BAT0"
	}
	return &Sysfs{root: root, bat: bat}
}

func (s *Sysfs) batPath(attr string) string { return filepath.Join(s.root, s.bat, attr) }

// readStr reads a sysfs attribute and trims the trailing newline.
func readStr(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimRight(string(b), "\n"), nil
}

// readInt reads an integer attribute. A missing optional attribute is reported
// by the caller via os.IsNotExist on the returned error.
func readInt(path string) (int64, error) {
	str, err := readStr(path)
	if err != nil {
		return 0, err
	}
	v, err := strconv.ParseInt(str, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", path, err)
	}
	return v, nil
}

// optInt reads an integer attribute that may legitimately be absent, returning
// 0 when the file does not exist. A real read error still propagates.
func optInt(path string) (int64, error) {
	v, err := readInt(path)
	if err != nil && os.IsNotExist(err) {
		return 0, nil
	}
	return v, err
}

// writeStr writes a value to a sysfs attribute.
func writeStr(path, val string) error {
	if err := os.WriteFile(path, []byte(val), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func (s *Sysfs) Read() (Reading, error) {
	var r Reading

	cap, err := readInt(s.batPath("capacity"))
	if err != nil {
		return r, fmt.Errorf("read capacity: %w", err)
	}
	r.Capacity = int(cap)

	status, err := readStr(s.batPath("status"))
	if err != nil {
		return r, fmt.Errorf("read status: %w", err)
	}
	r.Status = Status(status)

	for _, f := range []struct {
		attr string
		dst  *int64
	}{
		{"energy_now", &r.EnergyNowUWh},
		{"energy_full", &r.EnergyFullUWh},
		{"energy_full_design", &r.EnergyFullDesignUWh},
		{"voltage_now", &r.VoltageNowUV},
	} {
		v, err := optInt(s.batPath(f.attr))
		if err != nil {
			return r, fmt.Errorf("read %s: %w", f.attr, err)
		}
		*f.dst = v
	}
	cc, err := optInt(s.batPath("cycle_count"))
	if err != nil {
		return r, fmt.Errorf("read cycle_count: %w", err)
	}
	r.CycleCount = int(cc)

	online, err := s.acOnline()
	if err != nil {
		return r, err
	}
	r.ACOnline = online

	return r, nil
}

// acOnline is true when any Mains-type supply reports online. Scanning by type
// avoids hardcoding the supply name (AC vs ADP1 vs others across models).
func (s *Sysfs) acOnline() (bool, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return false, fmt.Errorf("list %s: %w", s.root, err)
	}
	for _, e := range entries {
		typ, err := readStr(filepath.Join(s.root, e.Name(), "type"))
		if err != nil || typ != "Mains" {
			continue
		}
		on, err := optInt(filepath.Join(s.root, e.Name(), "online"))
		if err != nil {
			return false, fmt.Errorf("read %s online: %w", e.Name(), err)
		}
		if on == 1 {
			return true, nil
		}
	}
	return false, nil
}

func (s *Sysfs) HasThresholds() bool {
	_, err := os.Stat(s.batPath("charge_control_start_threshold"))
	return err == nil
}

func (s *Sysfs) Thresholds() (start, stop int, err error) {
	st, err := readInt(s.batPath("charge_control_start_threshold"))
	if err != nil {
		return 0, 0, fmt.Errorf("read start threshold: %w", err)
	}
	sp, err := readInt(s.batPath("charge_control_end_threshold"))
	if err != nil {
		return 0, 0, fmt.Errorf("read stop threshold: %w", err)
	}
	return int(st), int(sp), nil
}

func (s *Sysfs) SetThresholds(start, stop int) error {
	if err := writeStr(s.batPath("charge_control_end_threshold"), strconv.Itoa(stop)); err != nil {
		return err
	}
	return writeStr(s.batPath("charge_control_start_threshold"), strconv.Itoa(start))
}

func (s *Sysfs) Supported() ([]Behaviour, error) {
	avail, _, err := s.readBehaviour()
	if err != nil {
		return nil, err
	}
	return avail, nil
}

func (s *Sysfs) Behaviour() (Behaviour, error) {
	_, active, err := s.readBehaviour()
	return active, err
}

// readBehaviour parses charge_behaviour, e.g. "[auto] inhibit-charge
// force-discharge": every token is available, the bracketed one is active. A
// machine with no charge_behaviour attribute reports only auto, active.
func (s *Sysfs) readBehaviour() (avail []Behaviour, active Behaviour, err error) {
	str, err := readStr(s.batPath("charge_behaviour"))
	if err != nil {
		if os.IsNotExist(err) {
			return []Behaviour{BehaviourAuto}, BehaviourAuto, nil
		}
		return nil, "", fmt.Errorf("read charge_behaviour: %w", err)
	}
	for _, tok := range strings.Fields(str) {
		if strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]") {
			tok = strings.TrimSuffix(strings.TrimPrefix(tok, "["), "]")
			active = Behaviour(tok)
		}
		avail = append(avail, Behaviour(tok))
	}
	if len(avail) == 0 {
		return nil, "", fmt.Errorf("charge_behaviour empty: %q", str)
	}
	return avail, active, nil
}

func (s *Sysfs) SetBehaviour(b Behaviour) error {
	return writeStr(s.batPath("charge_behaviour"), string(b))
}
