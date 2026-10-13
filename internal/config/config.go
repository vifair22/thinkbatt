// Package config loads thinkbatt's settings from a small INI file. A full TOML
// parser would outweigh an eight-key config and pull a dependency into an
// otherwise stdlib-only tree, so the format is flat "key = value" lines parsed
// by hand. Every key has a default, so a missing file is not an error.
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Config is the daemon's runtime policy.
type Config struct {
	BandStart        int    // resume-charging threshold, percent
	BandStop         int    // stop-charging threshold, percent (the resting ceiling)
	CalibFloor       int    // lowest state of charge a calibration discharges to
	CalibEveryCycles int    // run calibration after this many cycles since the last
	CalibEveryDays   int    // or after this many days since the last, whichever first
	CalibEnabled     bool   // master switch for automatic calibration
	SocketGroup      string // group that may use the control socket; empty = world-accessible

	RestAtFullMinutes     int     // relaxation at full before discharging (0 = skip)
	RestAtFloorMinutes    int     // relaxation at the floor before recharging (0 = skip)
	DischargeCRate        float64 // discharge rate, fraction of design capacity per hour (0 = idle)
	ChargeTimeoutHours    int     // bound on each charge phase
	DischargeTimeoutHours int     // bound on each discharge phase
}

// Default returns the built-in policy used when a key or the whole file is
// absent. The floor sits above UPower's 2% hibernate action so a calibration
// discharge never trips the OS low-battery handler.
func Default() Config {
	return Config{
		BandStart:             75,
		BandStop:              80,
		CalibFloor:            7,
		CalibEveryCycles:      30,
		CalibEveryDays:        90,
		CalibEnabled:          true,
		RestAtFullMinutes:     60,
		RestAtFloorMinutes:    60,
		DischargeCRate:        0.2,
		ChargeTimeoutHours:    5,
		DischargeTimeoutHours: 8,
	}
}

// File is a parsed config: the global policy plus any per-pack overrides. A
// machine with one battery never needs a section; a second pack that wants a
// different band or no automatic calibration gets a "[BATx]" section whose keys
// override the global ones for that pack only. Resolve a pack's effective
// policy with For.
type File struct {
	Global    Config
	overrides map[string][]kv // pack name -> raw key/vals from its [section]
}

type kv struct{ key, val string }

// socketGroupKey is global-only: the control socket belongs to the daemon, not
// to any one pack, so it is rejected inside a pack section.
const socketGroupKey = "socket_group"

// Load reads the config at path. A path that does not exist yields the defaults
// unchanged, which is the normal first-boot case. It returns only the global
// policy; use LoadFile when per-pack overrides matter.
func Load(path string) (Config, error) {
	f, err := LoadFile(path)
	return f.Global, err
}

// LoadFile reads the config at path, sections and all. A nonexistent path
// yields the defaults, the normal first-boot case.
func LoadFile(path string) (File, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return File{Global: Default()}, nil
		}
		return File{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return ParseFile(f)
}

// Parse reads global INI key=value lines over the defaults and validates the
// result, ignoring any per-pack sections. It is the flat view of ParseFile.
func Parse(r io.Reader) (Config, error) {
	f, err := ParseFile(r)
	return f.Global, err
}

// ParseFile reads an INI body into a File. Lines before any section header, or
// under a "[global]" header, set the global policy; lines under a "[BATx]"
// header override that policy for pack BATx. Blank lines and lines beginning
// with '#' or ';' are ignored. Every override is resolved and validated here so
// a bad section fails at load, not at the first rescan.
func ParseFile(r io.Reader) (File, error) {
	global := Default()
	overrides := map[string][]kv{}
	section := "" // "" is the global section
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			if section == "" {
				return File{}, fmt.Errorf("line %d: empty section header", ln)
			}
			if strings.EqualFold(section, "global") {
				section = ""
			}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return File{}, fmt.Errorf("line %d: not key=value: %q", ln, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if section == "" {
			if err := global.set(key, val); err != nil {
				return File{}, fmt.Errorf("line %d: %w", ln, err)
			}
			continue
		}
		if key == socketGroupKey {
			return File{}, fmt.Errorf("line %d: %s is global-only, not valid in [%s]", ln, socketGroupKey, section)
		}
		overrides[section] = append(overrides[section], kv{key, val})
	}
	if err := sc.Err(); err != nil {
		return File{}, fmt.Errorf("read config: %w", err)
	}
	if err := global.Validate(); err != nil {
		return File{}, err
	}
	f := File{Global: global, overrides: overrides}
	for name := range overrides {
		if _, err := f.For(name); err != nil {
			return File{}, fmt.Errorf("section [%s]: %w", name, err)
		}
	}
	return f, nil
}

// For resolves the effective policy for a pack: the global policy with that
// pack's section applied over it. A pack with no section gets the global policy
// unchanged.
func (f File) For(pack string) (Config, error) {
	c := f.Global
	for _, o := range f.overrides[pack] {
		if err := c.set(o.key, o.val); err != nil {
			return Config{}, err
		}
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// OverriddenPacks returns the pack names that carry an explicit section, so the
// daemon can log which packs deviate from the global policy. Discovery is still
// automatic; a section only tunes a pack, it does not declare one.
func (f File) OverriddenPacks() []string {
	names := make([]string, 0, len(f.overrides))
	for name := range f.overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (c *Config) set(key, val string) error {
	switch key {
	case "band_start":
		return assignInt(&c.BandStart, key, val)
	case "band_stop":
		return assignInt(&c.BandStop, key, val)
	case "calib_floor":
		return assignInt(&c.CalibFloor, key, val)
	case "calib_every_cycles":
		return assignInt(&c.CalibEveryCycles, key, val)
	case "calib_every_days":
		return assignInt(&c.CalibEveryDays, key, val)
	case "calib_enabled":
		b, err := strconv.ParseBool(val)
		if err != nil {
			return fmt.Errorf("%s: %q is not a boolean", key, val)
		}
		c.CalibEnabled = b
		return nil
	case "socket_group":
		c.SocketGroup = val
		return nil
	case "rest_at_full_minutes":
		return assignInt(&c.RestAtFullMinutes, key, val)
	case "rest_at_floor_minutes":
		return assignInt(&c.RestAtFloorMinutes, key, val)
	case "discharge_c_rate":
		return assignFloat(&c.DischargeCRate, key, val)
	case "charge_timeout_hours":
		return assignInt(&c.ChargeTimeoutHours, key, val)
	case "discharge_timeout_hours":
		return assignInt(&c.DischargeTimeoutHours, key, val)
	default:
		return fmt.Errorf("unknown key %q", key)
	}
}

func assignFloat(dst *float64, key, val string) error {
	v, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return fmt.Errorf("%s: %q is not a number", key, val)
	}
	*dst = v
	return nil
}

func assignInt(dst *int, key, val string) error {
	v, err := strconv.Atoi(val)
	if err != nil {
		return fmt.Errorf("%s: %q is not an integer", key, val)
	}
	*dst = v
	return nil
}

// Validate rejects a policy that could misbehave or endanger the pack.
func (c Config) Validate() error {
	if c.BandStart < 1 || c.BandStart > 99 {
		return fmt.Errorf("band_start %d out of range 1-99", c.BandStart)
	}
	if c.BandStop < 2 || c.BandStop > 100 {
		return fmt.Errorf("band_stop %d out of range 2-100", c.BandStop)
	}
	if c.BandStart >= c.BandStop {
		return fmt.Errorf("band_start %d must be below band_stop %d", c.BandStart, c.BandStop)
	}
	// The floor must clear UPower's default 2% hibernate action with margin and
	// never be so high the discharge leg is pointless.
	if c.CalibFloor < 5 || c.CalibFloor > 50 {
		return fmt.Errorf("calib_floor %d out of range 5-50", c.CalibFloor)
	}
	if c.CalibFloor >= c.BandStart {
		return fmt.Errorf("calib_floor %d must be below band_start %d", c.CalibFloor, c.BandStart)
	}
	if c.CalibEveryCycles < 1 {
		return fmt.Errorf("calib_every_cycles %d must be at least 1", c.CalibEveryCycles)
	}
	if c.CalibEveryDays < 1 {
		return fmt.Errorf("calib_every_days %d must be at least 1", c.CalibEveryDays)
	}
	if c.RestAtFullMinutes < 0 || c.RestAtFullMinutes > 720 {
		return fmt.Errorf("rest_at_full_minutes %d out of range 0-720", c.RestAtFullMinutes)
	}
	if c.RestAtFloorMinutes < 0 || c.RestAtFloorMinutes > 720 {
		return fmt.Errorf("rest_at_floor_minutes %d out of range 0-720", c.RestAtFloorMinutes)
	}
	// A C-rate above 2C is implausible on a laptop and likely a typo; 0 disables
	// the assist (idle discharge), which then needs a generous discharge timeout.
	if c.DischargeCRate < 0 || c.DischargeCRate > 2 {
		return fmt.Errorf("discharge_c_rate %.3f out of range 0-2", c.DischargeCRate)
	}
	if c.ChargeTimeoutHours < 1 {
		return fmt.Errorf("charge_timeout_hours %d must be at least 1", c.ChargeTimeoutHours)
	}
	if c.DischargeTimeoutHours < 1 {
		return fmt.Errorf("discharge_timeout_hours %d must be at least 1", c.DischargeTimeoutHours)
	}
	return nil
}
