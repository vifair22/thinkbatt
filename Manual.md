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

The fix is a learning cycle: charge to full, discharge to a low floor, charge to
full again. That gives the gauge the endpoints it needs to re-anchor FCC.
thinkbatt automates this using the firmware's `force-discharge`, so the pack is
driven down on AC in a few hours rather than coasting at a high charge for weeks.

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

## The calibration cycle

When due, the daemon runs these phases, each bounded by a timeout:

1. **Charge to full** - thresholds set to 99/100, wait for full.
2. **Discharge to the floor** - `force-discharge`, wait until charge <= `calib_floor`.
3. **Recharge to full** - back to auto at 99/100, wait for full. This closes the
   learning cycle so the gauge finalizes FCC.
4. **Return to the band** - force-discharge back down to `band_stop`, then
   restore the band and auto.

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

A full cycle is roughly 6-8 hours of wall time (two full charges plus a deep
discharge at laptop power). Keep the machine on AC.

## When it runs

A calibration is due when either `calib_every_cycles` charge cycles have passed
since the last, or `calib_every_days` days have, whichever comes first (defaults
30 cycles / 90 days). The cycle arm catches a heavily used pack; the time arm
catches a pack that barely cycles. The daemon re-checks on a timer
(`--check-interval`, default hourly). The first run after install, with no prior
calibration recorded, is due immediately.

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
