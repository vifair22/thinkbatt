# thinkbatt

Battery charge-policy and fuel-gauge calibration daemon for ThinkPads.

It owns the resting charge band (so the pack spends its life in a narrow,
low-wear window), and runs a periodic fuel-gauge **learning cycle** - charge
full, rest, force-discharge to a floor at a controlled rate, rest, recharge full
- so the pack's gauge re-anchors its learned full-charge capacity instead of
drifting. A control CLI reports status and drives it by hand.

It is one binary for the whole fleet: it detects at runtime what a given machine
can do and does only that. A coreboot ThinkPad that exposes `force-discharge`
(see the thinkpad-t430 firmware notes) gets the full learning cycle; a machine
with only charge thresholds gets band management; a machine with neither is left
alone.

Status: pre-1.0, developed on `master`. Calibration is validated against a
software battery simulator; the first hardware learning cycle is supervised (see
the Manual).

## Layout

| Path | What |
|------|------|
| `cmd/thinkbattd/` | the daemon: owns the band, schedules calibration, serves the control socket |
| `cmd/thinkbatt/` | the control CLI |
| `internal/battery/` | the sysfs hardware surface and an in-memory simulator for tests |
| `internal/caps/` | runtime capability detection |
| `internal/config/` | INI config |
| `internal/calib/` | the calibration state machine |
| `internal/policy/` | band assertion and the calibration due-decision |
| `internal/control/` | the unix-socket control protocol |
| `internal/daemon/` | orchestration behind the socket |
| `internal/state/` | persisted calibration history |
| `packaging/` | the systemd unit and the example config |
| `Manual.md` | operational manual: the cycle, safety, config, CLI, troubleshooting |

## Build and test

```sh
make            # build build/bin/thinkbattd and build/bin/thinkbatt
make test       # go test -race ./...
make coverage   # coverage profile with the 80% gate
make analyze    # go vet, gofmt, golangci-lint
make check      # ascii-check
```

Go 1.26, standard toolchain, no third-party dependencies.

## Install

```sh
sudo make install                                   # binaries into /usr/local/bin
sudo install -Dm644 packaging/thinkbatt.ini /etc/thinkbatt/thinkbatt.ini
sudo install -Dm644 packaging/systemd/thinkbattd.service \
    /etc/systemd/system/thinkbattd.service
sudo systemctl enable --now thinkbattd
```

On a machine where thinkbatt owns the band, remove any older charge-threshold
udev rule (e.g. the thinkpad-t430 `80-t430-battery.rules`) so the two do not
both write the thresholds. See the Manual.

## Usage

```sh
thinkbatt status              # charge, band, capabilities, last calibration
thinkbatt calibrate --now     # run a learning cycle now
thinkbatt cancel              # stop a running calibration (restores the band)
thinkbatt band 75 80          # set the resting band at runtime
```
