// Command thinkbatt is the control client for thinkbattd. It speaks the control
// protocol over the daemon's unix socket: show status, trigger or cancel a
// calibration, or change the resting band.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"

	"git.airies.net/vifair22/thinkbatt/internal/control"
)

var version = "dev"

const defaultSocket = "/run/thinkbattd/control.sock"

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
		return cmdStatus(client)
	case "calibrate":
		return cmdCalibrate(client, rest)
	case "cancel":
		return cmdSimple(client, control.CmdCancel, "calibration cancelled")
	case "band":
		return cmdBand(client, rest)
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

func cmdStatus(c control.Client) error {
	resp, err := c.Do(control.Request{Cmd: control.CmdStatus})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	var info control.StatusInfo
	if err := json.Unmarshal(resp.Data, &info); err != nil {
		return fmt.Errorf("decode status: %w", err)
	}
	printStatus(info)
	return nil
}

func printStatus(info control.StatusInfo) {
	fmt.Printf("charge:      %d%% (%s)\n", info.Capacity, info.Status)
	fmt.Printf("behaviour:   %s\n", info.BehaviourActive)
	if info.HasThresholds {
		fmt.Printf("band:        %d-%d%%\n", info.BandStart, info.BandStop)
	} else {
		fmt.Printf("band:        unavailable (no charge thresholds)\n")
	}
	if info.EnergyFullUWh > 0 {
		fmt.Printf("gauge full:  %.1f Wh (design %.1f Wh)\n",
			float64(info.EnergyFullUWh)/1e6, float64(info.EnergyFullDesignUWh)/1e6)
	}
	fmt.Printf("caps:        %s\n", info.CapsSummary)
	fmt.Printf("calibrating: %t\n", info.CalibrationRunning)
	if lc := info.LastCalibration; lc != nil {
		fmt.Printf("last calib:  %s, %s (%s)\n",
			lc.FinishedAt.Format("2006-01-02 15:04"), lc.Outcome, lc.Reason)
		if lc.EnergyFullBeforeUWh > 0 || lc.EnergyFullAfterUWh > 0 {
			fmt.Printf("  gauge full: %.1f -> %.1f Wh\n",
				float64(lc.EnergyFullBeforeUWh)/1e6, float64(lc.EnergyFullAfterUWh)/1e6)
		}
	} else {
		fmt.Printf("last calib:  never\n")
	}
}

func cmdCalibrate(c control.Client, args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	now := fs.Bool("now", false, "run immediately, bypassing the due-check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	resp, err := c.Do(control.Request{Cmd: control.CmdCalibrate, Args: map[string]string{"now": strconv.FormatBool(*now)}})
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
	if len(args) != 2 {
		return fmt.Errorf("usage: thinkbatt band <start> <stop>")
	}
	if _, err := strconv.Atoi(args[0]); err != nil {
		return fmt.Errorf("start %q is not an integer", args[0])
	}
	if _, err := strconv.Atoi(args[1]); err != nil {
		return fmt.Errorf("stop %q is not an integer", args[1])
	}
	resp, err := c.Do(control.Request{Cmd: control.CmdBand, Args: map[string]string{"start": args[0], "stop": args[1]}})
	if err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("%s", resp.Error)
	}
	fmt.Printf("resting band set to %s-%s%% (runtime only; edit the config to persist)\n", args[0], args[1])
	return nil
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
  thinkbatt status              show charge, band, capabilities, last calibration
  thinkbatt calibrate [--now]   run a calibration (--now bypasses the due-check)
  thinkbatt cancel              stop a running calibration
  thinkbatt band <start> <stop> set the resting band (runtime only)
  thinkbatt version             print version

The daemon socket defaults to `+defaultSocket+`; override with THINKBATT_SOCKET.
`)
}
