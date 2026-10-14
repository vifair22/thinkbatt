# thinkbatt operational manual

thinkbatt manages ThinkPad battery charging in two ways: it holds the pack in a
low-wear resting band, and it periodically runs a fuel-gauge learning cycle so
the pack's reported capacity stays honest. This manual covers what it does, how
to operate it, and how to verify it.

## Why a learning cycle

A lithium pack's gauge estimates its full-charge capacity (FCC) by watching full
charges and deep discharges. A pack held in a narrow band (say 75-80%) never
sees either endpoint, so its FCC estimate drifts: a gauge can report a full
charge well above the pack's design capacity, which is physically impossible and
makes every percentage and time-remaining figure untrustworthy.

The fix is a learning cycle: charge to full, rest, discharge to a low floor,
rest, charge to full again. The gauges in these packs (TI impedance-track class)
relearn FCC from two relaxed open-circuit-voltage readings, one at full and one
near empty, so the cycle rests the pack at each turnaround (`inhibit-charge`,
neither charging nor discharging on AC) long enough for the voltage to settle;
without those rests the learned capacity does not move. thinkbatt drives the
discharge with the firmware's `force-discharge`, held at a configured C-rate by
a controlled CPU load, so the pack comes down on AC in a bounded time rather than
coasting at a high charge for weeks or crawling at the idle draw.

## Capability model

thinkbatt reads the kernel power_supply sysfs and adapts per machine:

| Machine exposes | thinkbatt does |
|-----------------|----------------|
| `charge_control_*_threshold` and `charge_behaviour` with `force-discharge` | band management + full learning cycle |
| thresholds only | band management; calibration disabled |
| neither | nothing (logs why) |

`thinkbatt status` prints the detected capabilities. A coreboot ThinkPad with
the H8 charge-behaviour methods enabled has the full set; a stock-firmware
machine typically has thresholds only.

## The resting band

The daemon writes the band (default 75/80) and `charge_behaviour=auto` at
startup and after every calibration. It is the band's owner: on a machine that
previously set the band from a udev rule, remove that rule so the two do not
fight. If the daemon is not running, the EC keeps the last thresholds it was
given, so the band is safe across a daemon restart.

`thinkbatt band <start> <stop>` changes the band at runtime. It is not written
back to the config; edit `/etc/thinkbatt/thinkbatt.ini` to make it permanent.
With more than one pack it sets every band-capable pack unless `--battery BATx`
names one.

## Multiple batteries

thinkbatt manages every system battery the machine presents, not just the
internal pack. A ThinkPad with a bay or external pack shows it as `BAT1` beside
the internal `BAT0`. Peripheral batteries (a Bluetooth mouse or keyboard, which
the kernel also exposes as `type=Battery` but marks `scope=Device`) are not
packs and are skipped. Packs are **discovered automatically** and reconciled on each
maintenance pass: a pack docked while the daemon runs is picked up and banded
within one interval, and a pack pulled is dropped. Each pack is tracked
independently - its own capability detection, its own resting band, and its own
calibration history and due-schedule.

Per-pack settings go in a section named for the pack; any key left out falls
back to the global value. For example, to rest a small external pack in a lower
band and never auto-calibrate it:

```ini
[BAT1]
band_start = 50
band_stop  = 60
calib_enabled = false
```

`socket_group` is global and is not valid in a section. A section only tunes a
pack; it does not declare one (discovery does that), so a section for a pack
that is never present is simply inert.

Calibration is still **one at a time** across the whole machine - the
force-discharge assist and the sleep inhibitor are shared. With several packs
present, `thinkbatt calibrate` requires `--battery BATx`; it will not pick a
pack to deep-discharge on its own. Automatic (due-based) calibration walks the
packs in name order and starts the first that is enabled, capable, and due.

A note on hardware from the T480's external pack: `force-discharge` is accepted
only on AC (on battery the firmware ignores it, and the kernel reports EIO), and
on battery the EC alone decides which pack drains. Calibration runs on AC, where
per-pack force-discharge drives the chosen pack, so neither limit affects it.

## The calibration cycle

When due, the daemon runs these phases, each bounded by a timeout:

1. **Charge to full** - thresholds set to 99/100, wait for full (a pack already
   near full counts as done once the charger stops, so it does not stall).
2. **Rest at full** - `inhibit-charge` for `rest_at_full_minutes`, so the pack
   relaxes and the gauge takes its full-charge OCV reading.
3. **Discharge to the floor** - `force-discharge` held at `discharge_c_rate` by
   the controlled load, wait until charge <= `calib_floor`.
4. **Rest at the floor** - `inhibit-charge` for `rest_at_floor_minutes`, so the
   gauge takes its near-empty OCV reading. Together with step 2 this is what
   lets the gauge relearn FCC.
5. **Recharge to full** - back to auto at 99/100, wait for full.
6. **Return to the band** - force-discharge back down to `band_stop`, then
   restore the band and auto.

A machine that lacks `inhibit-charge` skips the rests (logged); the cycle still
runs but the relearn is weaker.

Safety invariants:

- **The band is always restored.** However the run ends - success, timeout,
  error, cancellation, or `systemctl stop` - the daemon returns the pack to auto
  behaviour and the resting band. It never leaves the pack force-discharging or
  stranded at full.
- **AC is required.** The run aborts if AC is not present at the start, or if the
  adapter is unplugged mid-cycle.
- **The floor clears the OS low-battery action.** The default floor (7%) sits
  above UPower's 2% hibernate action, so the discharge never trips a suspend.
- **Sleep is inhibited** for the duration via `systemd-inhibit`, so closing the
  lid does not derail a run.
- **One at a time.** A second calibration request while one is running is refused.

A full cycle is roughly 8-11 hours of wall time at the default C/5 discharge:
two full charges, a deep discharge, and the two rests. The controlled discharge
is what keeps it bounded; `discharge_c_rate = 0` reverts to the idle draw, where
the discharge alone can exceed 12 hours (raise `discharge_timeout_hours` to
match). Keep the machine on AC throughout.

## When it runs

A calibration is due when either `calib_every_cycles` charge cycles have passed
since the last, or `calib_every_days` days have, whichever comes first (defaults
30 cycles / 90 days). The cycle arm catches a heavily used pack; the time arm
catches a pack that barely cycles. The daemon re-checks on a timer
(`--maintain-interval`, default 2 minutes), the same pass that rediscovers packs
and re-asserts the bands. The first run after install, with no prior calibration
recorded for a pack, is due immediately.

Force a run regardless of the schedule with `thinkbatt calibrate --now`.
`thinkbatt cancel` stops a run; the band is restored as it unwinds.

## Configuration

`/etc/thinkbatt/thinkbatt.ini`, INI `key = value`. See `packaging/thinkbatt.ini`
for every key and its default. A missing file means all defaults.

| Key | Default | Meaning |
|-----|---------|---------|
| `band_start` | 75 | resume-charging threshold |
| `band_stop` | 80 | stop-charging threshold (the resting ceiling) |
| `calib_floor` | 7 | discharge target for a learning cycle |
| `calib_every_cycles` | 30 | cycles between calibrations |
| `calib_every_days` | 90 | days between calibrations |
| `calib_enabled` | true | automatic calibration on/off (manual `--now` still works) |
| `rest_at_full_minutes` | 60 | relaxation at full for the gauge's OCV reading (0 skips) |
| `rest_at_floor_minutes` | 60 | relaxation at the floor for the gauge's OCV reading (0 skips) |
| `discharge_c_rate` | 0.2 | discharge rate as a fraction of design capacity/hour (0 = idle draw) |
| `charge_timeout_hours` | 5 | bound on each charge phase |
| `discharge_timeout_hours` | 8 | bound on each discharge phase |

Every key above is global by default. A `[BATx]` section overrides any of them
for one pack (except `socket_group`, which is global-only); see Multiple
batteries.

## Verifying a calibration worked

Note `energy_full` before and after (`thinkbatt status` shows the gauge's full
and design figures; the last-calibration line shows before -> after). A
successful learning cycle moves `energy_full` off an inflated value toward the
pack's design capacity. One cycle may not fully converge; the gauge improves
over successive learning cycles.

Supervise the first run on a machine: trigger it with `thinkbatt calibrate
--now` on AC, watch `journalctl -u thinkbattd -f` through the phases, and
confirm the pack returns to the band at the end. Confirm `thinkbatt cancel` and
`systemctl stop thinkbattd` each restore the band immediately.

## Troubleshooting

- **`thinkbatt status` cannot connect** - the daemon is not running, or the
  socket path differs. Check `systemctl status thinkbattd` and
  `journalctl -u thinkbattd`.
- **Calibration refused with "cannot calibrate"** - the machine does not expose
  `force-discharge`. Band management still works; the full cycle does not.
- **Calibration keeps aborting "AC lost"** - a flaky adapter or dock; the run
  needs AC throughout.
- **Band not holding** - another actor (an old udev rule, TLP) is also writing
  the thresholds. Remove it; thinkbatt is the owner.
