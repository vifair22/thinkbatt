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

It manages **every** battery present, not just the internal one. Packs are
discovered automatically and tracked independently - a docked or bay pack (BAT1
beside BAT0) is banded as soon as it appears and dropped when pulled, each pack
keeps its own calibration history, and a `[BAT1]` section in the config gives a
pack its own band or disables its automatic calibration. One calibration runs at
a time, against a chosen pack.

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
thinkbatt status                         # every pack: charge, band, caps, last calibration
thinkbatt calibrate --now                # run a learning cycle now (one pack only)
thinkbatt calibrate --battery BAT1 --now # ...name the pack when more than one is present
thinkbatt cancel                         # stop the running calibration (restores the band)
thinkbatt band 75 80                     # set the resting band at runtime (all packs)
thinkbatt band --battery BAT1 50 60      # ...or just one pack
```

With more than one pack present, `calibrate` requires `--battery` rather than
guessing which pack to drain.
