// Command thinkbatt is the control client for thinkbattd. It speaks the control
// protocol over the daemon's unix socket: show status, trigger or cancel a
// calibration, change the resting band, or set a temporary charge override.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"git.airies.net/vifair22/thinkbatt/internal/control"
	"git.airies.net/vifair22/thinkbatt/internal/state"
)

var version = "dev"

const defaultSocket = "/run/thinkbattd/control.sock"

// now is the wall clock the --until parser resolves "09:00" against; tests pin it.
var now = time.Now

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "thinkbatt: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	socket := defaultSocket
	if s := os.Getenv("THINKBATT_SOCKET"); s != "" {
		socket = s
	}

	if len(args) == 0 {
		usage(os.Stderr)
		return fmt.Errorf("no command given")
	}

	client := control.Client{Path: socket}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "status":
		return cmdStatus(client, rest)
	case "calibrate":
		return cmdCalibrate(client, rest)
	case "cancel":
		return cmdSimple(client, control.CmdCancel, "calibration cancelled")
	case "band":
		return cmdBand(client, rest)
	case "charge":
		return cmdCharge(client, rest)
	case "version":
		fmt.Printf("thinkbatt %s\n", version)
		return nil
	case "help", "-h", "--help":
		usage(os.Stdout)
		return nil
	default:
		usage(os.Stderr)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

func cmdStatus(c control.Client, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the daemon's status payload as JSON (for scripts and the plasmoid)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: thinkbatt status [--json]")
	}
	resp, err := c.Do(control.Request{Cmd: control.CmdStatus})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	if *asJSON {
		fmt.Println(string(resp.Data))
		return nil
	}
	var info control.StatusInfo
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		return fmt.Errorf("decode status: %w", err)
	}
	printStatus(info)
	return nil
}

func printStatus(info control.StatusInfo) {
	if len(info.Packs) == 0 {
		fmt.Println("no battery packs present")
		return
	}
	for i, p := range info.Packs {
		if i > 0 {
			fmt.Println()
		}
		printPack(p)
	}
}

func printPack(p control.PackStatus) {
	fmt.Printf("%s\n", p.Name)
	fmt.Printf("  charge:      %d%% (%s)\n", p.Capacity, p.Status)
	fmt.Printf("  power:       %s, %.2f V\n", describePower(p.PowerUW), float64(p.VoltageUV)/1e6)
	if eta := describeEstimate(p); eta != "" {
		fmt.Printf("  estimate:    %s\n", eta)
	}
	fmt.Printf("  behaviour:   %s\n", p.BehaviourActive)
	if p.HasThresholds {
		fmt.Printf("  band:        %d-%d%%\n", p.BandStart, p.BandStop)
	} else {
		fmt.Printf("  band:        unavailable (no charge thresholds)\n")
	}
	if p.EnergyFullUWh > 0 {
		fmt.Printf("  gauge full:  %.1f Wh (design %.1f Wh)\n",
			float64(p.EnergyFullUWh)/1e6, float64(p.EnergyFullDesignUWh)/1e6)
	}
	if o := p.Override; o != nil {
		fmt.Printf("  override:    charge to %d%% %s\n", o.Target, describeUntil(o))
	}
	fmt.Printf("  caps:        %s\n", p.CapsSummary)
	if p.CalibrationRunning && p.CalibrationPhase != "" {
		fmt.Printf("  calibrating: true (%s)\n", p.CalibrationPhase)
	} else {
		fmt.Printf("  calibrating: %t\n", p.CalibrationRunning)
	}
	if lc := p.LastCalibration; lc != nil {
		fmt.Printf("  last calib:  %s, %s (%s)\n",
			lc.FinishedAt.Format("2006-01-02 15:04"), lc.Outcome, lc.Reason)
		if lc.EnergyFullBeforeUWh > 0 || lc.EnergyFullAfterUWh > 0 {
			fmt.Printf("    gauge full: %.1f -> %.1f Wh\n",
				float64(lc.EnergyFullBeforeUWh)/1e6, float64(lc.EnergyFullAfterUWh)/1e6)
		}
	} else {
		fmt.Printf("  last calib:  never\n")
	}
}

func cmdCalibrate(c control.Client, args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	now := fs.Bool("now", false, "run immediately, bypassing the due-check")
	bat := fs.String("battery", "", "pack to calibrate (required when more than one is present)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := c.Do(control.Request{Cmd: control.CmdCalibrate, Args: map[string]string{
		"now":     strconv.FormatBool(*now),
		"battery": *bat,
	}})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Println("calibration started")
	return nil
}

func cmdBand(c control.Client, args []string) error {
	fs := flag.NewFlagSet("band", flag.ContinueOnError)
	bat := fs.String("battery", "", "pack to set (default: every band-capable pack)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) != 2 {
		return fmt.Errorf("usage: thinkbatt band [--battery BATx] <start> <stop>")
	}
	if _, err := strconv.Atoi(rest[0]); err != nil {
		return fmt.Errorf("start %q is not an integer", rest[0])
	}
	if _, err := strconv.Atoi(rest[1]); err != nil {
		return fmt.Errorf("stop %q is not an integer", rest[1])
	}
	resp, err := c.Do(control.Request{Cmd: control.CmdBand, Args: map[string]string{
		"start":   rest[0],
		"stop":    rest[1],
		"battery": *bat,
	}})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	where := "every pack"
	if *bat != "" {
		where = *bat
	}
	fmt.Printf("resting band set to %s-%s%% on %s (runtime only; edit the config to persist)\n", rest[0], rest[1], where)
	return nil
}

// describePower renders the flow into the pack as watts in or out.
func describePower(uw int64) string {
	switch {
	case uw > 0:
		return fmt.Sprintf("%.1f W in", float64(uw)/1e6)
	case uw < 0:
		return fmt.Sprintf("%.1f W out", float64(-uw)/1e6)
	default:
		return "0.0 W"
	}
}

// describeEstimate renders the daemon's time estimate for a pack, or "" when
// it gave none.
func describeEstimate(p control.PackStatus) string {
	switch {
	case p.TimeToStopS > 0:
		return fmt.Sprintf("%d%% in %s", p.ChargeTarget, describeDuration(time.Duration(p.TimeToStopS)*time.Second))
	case p.TimeToEmptyS > 0:
		return fmt.Sprintf("empty in %s", describeDuration(time.Duration(p.TimeToEmptyS)*time.Second))
	default:
		return ""
	}
}

// describeDuration renders an estimate to the minute: "2h 05m", "12m", or
// "under a minute". Finer would claim a precision the estimate does not have.
func describeDuration(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	switch {
	case m < 1:
		return "under a minute"
	case m < 60:
		return fmt.Sprintf("%dm", m)
	default:
		return fmt.Sprintf("%dh %02dm", m/60, m%60)
	}
}

// describeUntil renders when an override ends, in local time.
func describeUntil(o *state.Override) string {
	if o.UntilFull {
		return "until full"
	}
	return "until " + o.Until.Local().Format("Mon 2006-01-02 15:04")
}

// cmdCharge sets or clears a temporary charge override. Flags may come before or
// after the target, so "charge full --until 09:00" reads naturally.
func cmdCharge(c control.Client, args []string) error {
	if len(args) > 0 && args[0] == "reset" {
		return cmdChargeReset(c, args[1:])
	}
	fs := flag.NewFlagSet("charge", flag.ContinueOnError)
	bat := fs.String("battery", "", "pack to override (default: every band-capable pack)")
	until := fs.String("until", control.UntilFull, `when the override ends: "full", "HH:MM", or "YYYY-MM-DD HH:MM"`)
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return fmt.Errorf("usage: thinkbatt charge [--battery BATx] [--until WHEN] <full|PERCENT>")
	}
	target, err := parseTarget(pos[0])
	if err != nil {
		return err
	}
	untilArg, when := control.UntilFull, "until full"
	if *until != control.UntilFull {
		t, err := parseUntil(*until, now())
		if err != nil {
			return err
		}
		untilArg = t.Format(time.RFC3339)
		when = "until " + t.Format("Mon 2006-01-02 15:04")
	}
	resp, err := c.Do(control.Request{Cmd: control.CmdCharge, Args: map[string]string{
		"battery": *bat,
		"target":  strconv.Itoa(target),
		"until":   untilArg,
	}})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	where := "every pack"
	if *bat != "" {
		where = *bat
	}
	fmt.Printf("charging %s to %d%% %s\n", where, target, when)
	return nil
}

func cmdChargeReset(c control.Client, args []string) error {
	fs := flag.NewFlagSet("charge reset", flag.ContinueOnError)
	bat := fs.String("battery", "", "pack to return to its band (default: every pack)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: thinkbatt charge reset [--battery BATx]")
	}
	resp, err := c.Do(control.Request{Cmd: control.CmdChargeReset, Args: map[string]string{"battery": *bat}})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Println("charge override cleared; back to the resting band")
	return nil
}

// parseInterspersed parses flags that may appear on either side of the
// positional arguments, which the flag package alone stops at.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// parseTarget accepts "full" (100) or a percentage.
func parseTarget(s string) (int, error) {
	if s == "full" {
		return 100, nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(s, "%"))
	if err != nil {
		return 0, fmt.Errorf("charge target %q is neither \"full\" nor a percentage", s)
	}
	return n, nil
}

// parseUntil resolves a deadline in local time. A bare "HH:MM" is its next
// occurrence: later today, or tomorrow if that time has passed. A date and
// time must be in the future; the daemon checks that too, but saying so here
// gives a clearer message.
func parseUntil(s string, ref time.Time) (time.Time, error) {
	loc := ref.Location()
	if t, err := time.ParseInLocation("15:04", s, loc); err == nil {
		at := time.Date(ref.Year(), ref.Month(), ref.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if !at.After(ref) {
			at = at.AddDate(0, 0, 1)
		}
		return at, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			if !t.After(ref) {
				return time.Time{}, fmt.Errorf("--until %s is in the past", s)
			}
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf(`--until %q: want "full", "HH:MM", or "YYYY-MM-DD HH:MM"`, s)
}

func cmdSimple(c control.Client, cmd, okMsg string) error {
	resp, err := c.Do(control.Request{Cmd: cmd})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Println(okMsg)
	return nil
}

func usage(w *os.File) {
	fmt.Fprint(w, `thinkbatt - control client for thinkbattd

usage:
  thinkbatt status [--json]                     show every pack: charge, band, override, caps, last calibration
  thinkbatt calibrate [--battery BATx] [--now]  calibrate a pack (--battery required if several present)
  thinkbatt cancel                              stop the running calibration
  thinkbatt band [--battery BATx] <start> <stop> set the resting band (runtime only; all packs by default)
  thinkbatt charge [--battery BATx] [--until WHEN] <full|PERCENT>
                                                charge above the band until WHEN, then return to it;
                                                WHEN is "full" (default), "HH:MM" (next occurrence)
                                                or "YYYY-MM-DD HH:MM"; cancels a running calibration
  thinkbatt charge reset [--battery BATx]       end a charge override now
  thinkbatt version                             print version

The daemon socket defaults to `+defaultSocket+`; override with THINKBATT_SOCKET.
`)
}
