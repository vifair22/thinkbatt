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
	"strconv"
	"strings"
)

// Config is the daemon's runtime policy.
type Config struct {
	BandStart        int  // resume-charging threshold, percent
	BandStop         int  // stop-charging threshold, percent (the resting ceiling)
	CalibFloor       int  // lowest state of charge a calibration discharges to
	CalibEveryCycles int  // run calibration after this many cycles since the last
	CalibEveryDays   int  // or after this many days since the last, whichever first
	CalibEnabled     bool // master switch for automatic calibration
}

// Default returns the built-in policy used when a key or the whole file is
// absent. The floor sits above UPower's 2% hibernate action so a calibration
// discharge never trips the OS low-battery handler.
func Default() Config {
	return Config{
		BandStart:        75,
		BandStop:         80,
		CalibFloor:       7,
		CalibEveryCycles: 30,
		CalibEveryDays:   90,
		CalibEnabled:     true,
	}
}

// Load reads the config at path. A path that does not exist yields the defaults
// unchanged, which is the normal first-boot case.
func Load(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return Config{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse reads INI key=value lines over the defaults and validates the result.
// Blank lines and lines beginning with '#' or ';' are ignored, as is a leading
// [section] header (the config is flat, but a header is tolerated).
func Parse(r io.Reader) (Config, error) {
	c := Default()
	sc := bufio.NewScanner(r)
	ln := 0
	for sc.Scan() {
		ln++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("line %d: not key=value: %q", ln, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if err := c.set(key, val); err != nil {
			return Config{}, fmt.Errorf("line %d: %w", ln, err)
		}
	}
	if err := sc.Err(); err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
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
	default:
		return fmt.Errorf("unknown key %q", key)
	}
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
	return nil
}
