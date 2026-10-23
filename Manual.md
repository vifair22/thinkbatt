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

Packs report their gauge in one of two unit systems, depending on the
firmware: energy (`energy_*` in uWh, `power_now`) or charge (`charge_*` in uAh,
`current_now`). The ThinkPads with an H8 EC report energy; the C14's ChromeEC
battery reports charge. thinkbatt converts a charge-unit pack to energy the way
upower does, charge x `voltage_min_design` (falling back to `voltage_now`), and
power as current x `voltage_now`, so status, calibration records and the
discharge assist's C-rate target work the same on both.

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

### Desktop charge limit settings

KDE Plasma's Power Management settings also offer a charge limit, written
through powerdevil's `chargethresholdhelper`. Leave it alone on a thinkbatt
machine. Powerdevil only writes the thresholds when Apply is pressed in that
page, so it does not fight thinkbatt on its own, but pressing Apply there
writes its own value, which thinkbatt then overwrites on its next pass.
Powerdevil also reads the thresholds once, when it starts, and does not notice
later changes: during a charge override, or after `thinkbatt band`, Plasma's
battery popup still reports the old "charge up to" figure. `thinkbatt status`
is authoritative.

## Charge overrides

A charge override holds packs at a higher (or lower) target for a while, then
returns them to the resting band on its own. It is the "I need a full battery
for tomorrow's trip" switch, and saves editing the band and remembering to put
it back.

```sh
thinkbatt charge full                          # top up to 100% once, then back to the band
thinkbatt charge full --until 09:00            # hold 100% until 09:00 (next occurrence)
thinkbatt charge 90 --until "2026-10-18 09:00" # hold 90% until a date and time
thinkbatt charge full --battery BAT1           # one pack only
thinkbatt charge reset                         # end it now
```

The target is applied as the threshold pair `target-1`/`target` (99/100 for
full). The start threshold has to move too: charging resumes only below start,
so raising the stop alone leaves a pack sitting at 78% under a 75/100 band
without charging at all.

How an override ends:

| `--until` | Ends |
|-----------|------|
| `full` (default) | when the pack reaches the target, or sits on AC with the charger stopped within 3 points of it (an EC may decline the last percent, as at the top of a calibration). Unplugging part way does not end it; it carries on when AC returns. |
| `HH:MM` | at the next occurrence of that local time: later today, or tomorrow if it has passed |
| `YYYY-MM-DD HH:MM` | at that local date and time, which must be in the future |

Expiry is checked on the maintenance pass, so an override ends within
`--maintain-interval` (2 minutes) of its deadline. Deadlines are wall-clock
time, so time spent suspended counts. The override is saved in the state file
and survives a reboot or daemon restart; a deadline that passed while the
machine was off ends it on the first pass after startup.

Ending an override restores the band but does not drain the pack. A pack
topped up to 100% stays near there on AC until it is used down to the band;
that is the point of "until full".

Interactions:

- **Calibration.** An override on a pack blocks its automatic calibration, and
  `calibrate --now` on it is refused until the override is cleared: a learning
  cycle drains to the floor, the opposite of what was asked. Setting an override
  on a pack that is calibrating cancels the run (recorded as aborted, "cancelled
  for a charge override"), waits for it to unwind, then applies the override.
- **`thinkbatt band`.** Changing the band during an override changes the band
  the pack returns to. The override stays in force.
- **Pulled packs.** An override is kept for a pack that is removed. A deadline
  override still expires on time; an until-full one waits for the pack to come
  back, or for `charge reset`.

## Time estimates

`thinkbatt status` gives each pack a time estimate when it is moving:

```
  estimate:    80% in 1h 23m      (charging)
  estimate:    empty in 3h 10m    (on battery)
```

The charge estimate runs to where charging actually stops, the
`charge_target` in `status --json`: the band's stop, an override's target, or
100% for a pack without thresholds and during a calibration's charging legs.
That is the point of computing it in the daemon. upower, and so Plasma's own
battery applet, assumes every pack charges to 100%, and with a 75-80 band its
"time until full" counts toward a charge the pack never reaches. The runtime
runs to 0%, as Plasma reports it; the session suspends at powerdevil's
critical level somewhat before that.

Both are linear: the energy left to the mark divided by the power. The power
is a moving average (a one-minute time constant), sampled every 10 s by the
daemon itself so the figure does not depend on how often a client asks.
`power_now` is steady while charging but follows CPU load on battery, and the
average keeps the runtime from jumping with every status read. It restarts
when the pack changes direction, so plugging in does not blend the discharge
rate into the charge estimate; until the next sample a fresh state uses the
instantaneous power. The status line's watts stay instantaneous.

There is no estimate (the field is omitted from the JSON) when the pack is
neither charging nor discharging, when the power is under 0.1 W, when the
result would exceed 48 hours, or during a calibration's discharge legs, whose
end is the calibration floor rather than empty. The charge estimate slightly
flatters a target of 100%, since the charger tapers over the last few percent;
at a band stop of 80 the taper has not begun.

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
`thinkbatt cancel` stops a run; the band is restored as it unwinds. A pack under
a charge override is never calibrated (see Charge overrides).

`thinkbatt status` shows the phase of a running calibration (charging, resting
at full, discharging, resting at floor, recharging, draining to band), and the
recorded reason for an aborted one says what cancelled it: the user, a charge
override, or daemon shutdown.

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

## Plasma tray plasmoid

`net.airies.thinkbatt` is a KDE Plasma 6 system-tray entry, meant to replace
Plasma's own battery applet on a machine thinkbatt manages: it shows the same
charge and time estimates (to where charging really stops) and has the same
sleep blocking. It is enabled in the tray by default; hide the stock one by
setting "Power and Battery" (`org.kde.plasma.battery`) to Disabled under
System Tray Settings -> Entries. Brightness is its own entry and is not
affected. Power profiles are not carried over; they stay in System Settings ->
Power Management.

Its icon follows the first pack's charge, with a badge when a pack is not
simply resting in its band: an arrow for a charge override, a refresh mark for
a running calibration, a warning when the daemon cannot be reached; or, with
none of those, the sleep-inhibited mark while sleep is blocked from the popup.
The tooltip has a line per pack, and says when sleep is blocked. A
middle-click on the icon toggles the sleep-only block (see Sleep blocking)
without opening the popup.

The popup shows, per pack:

- charge, status, and a bar with the band the pack is held at drawn over it
  (green for the resting band, the attention colour for an override)
- power in or out of the pack, the time estimate (see Time estimates), and its
  voltage
- health: the learned full-charge capacity against design
- the band, and the resting band behind an override
- calibration: the running phase, or the last run
- the charge-override form: a target (50% to full) and how it ends, which is
  until full, at a time (its next occurrence), or at a date and time picked from
  a calendar; or, with one active, what it is and a "Back to band" button
- a menu (the pack's overflow button) with "Calibrate now...", which asks for
  confirmation first, and "Cancel calibration"; while a run is going a Cancel
  button also sits under the readings

and, after the packs, the sleep blocking (see Sleep blocking below).

It does not speak the control protocol itself. It runs the `thinkbatt` CLI
(`status --json` every 3 s while open, every 30 s while closed, and the same
commands as the CLI for every action), so it needs exactly what the CLI needs:
the user must be able to reach the control socket. Errors from an action are
shown inline as the CLI printed them.

The command it runs is a setting (right-click -> Configure), `thinkbatt` by
default. It goes through a shell, so it can carry a path or an environment
prefix, which is how to point it at a development build:

```
env THINKBATT_SOCKET=/run/thinkbattd-test/control.sock ~/git/home/thinkbatt/build/bin/thinkbatt
```

For a quick look without adding it to the panel, `plasmawindowed
net.airies.thinkbatt` runs it in a window.

### Sleep blocking

"Block sleep and screen locking" holds off suspend, screen dimming and locking
until it is switched off, the same manual block Plasma's battery applet has.
"Block sleep only" holds off suspend alone: the screen still dims and locks on
its usual timers, for a download or a build that should finish with the lid
open but the session locked. A middle-click on the tray icon toggles the
sleep-only block.

Each is a powerdevil inhibition (PolicyAgent `AddInhibition`) filed as
`thinkbatt`: InterruptSession and ChangeScreenSettings with the reason
"Blocked from the thinkbatt tray", or InterruptSession alone with "Sleep
blocked from the thinkbatt tray". The two are independent; with both on, the
broader one wins. They live on plasmashell's D-Bus connection, so they end
when the session does, or when plasmashell restarts; the switches start off
again with them.

Below the switches is every other inhibition powerdevil knows about: which app,
what it holds off, and its stated reason. Each has a Block button that tells
powerdevil to ignore that request (`SetInhibitionAllowed`), so a stuck video
player or a forgotten download cannot keep the machine awake, and Allow to
undo it. powerdevil only lists an inhibition a few seconds after it is taken,
so a new one can take a moment to appear.

None of this goes through thinkbattd: it is per-session, and the daemon is
system-wide. The daemon's own sleep inhibitor during a calibration (a
`systemd-inhibit` lock) shows in the list like any other app's.

### Notifications

The plasmoid notifies when:

- a charge override ends (deadline reached, or the pack charged), unless it
  was ended from the popup's "Back to band"
- a calibration finishes, with the gauge's full capacity before and after
- a calibration fails or stops: AC lost part way, a phase timed out, or the
  daemon shut down. A cancel, and an override displacing the run, are the
  user's own doing and are not notified.

It keeps a record of what it last saw for each pack (the override and the last
calibration's finish time) in its configuration, and compares each status
against it. An end that happened while the plasmoid was not running, with the
machine suspended or the session logged out, is notified on the first status
after it is back. A pack seen for the first time is only recorded.

## Troubleshooting

- **`thinkbatt status` cannot connect** - the daemon is not running, or the
  socket path differs. Check `systemctl status thinkbattd` and
  `journalctl -u thinkbattd`.
- **Calibration refused with "cannot calibrate"** - the machine does not expose
  `force-discharge`. Band management still works; the full cycle does not.
- **Calibration keeps aborting "AC lost"** - a flaky adapter or dock; the run
  needs AC throughout.
- **Band not holding** - another actor (an old udev rule, TLP, Plasma's charge
  limit) is also writing the thresholds. Remove it; thinkbatt is the owner.
- **Plasma says "charge up to 80%" during an override** - powerdevil caches the
  threshold it read at login. Cosmetic; see Desktop charge limit settings.
- **`calibrate` refused with "has a charge override"** - clear it with
  `thinkbatt charge reset` first.
- **Plasmoid shows "Cannot reach thinkbattd"** - the same as the CLI failing:
  check the daemon is running and that the user is in `socket_group`. An error
  mentioning `--json` means the installed CLI predates 0.3.0.
- **No estimate, or no power reading, after an upgrade** - the package does
  not restart the daemon. `systemctl restart thinkbattd` (after
  `systemctl daemon-reload` if the unit changed); `thinkbatt status` from the
  new CLI against the old daemon simply lacks the new fields.
- **"Block sleep and screen locking" is disabled** - powerdevil is not running
  in the session (the popup says so). Plasma starts it; outside Plasma there is
  nothing for it to talk to.
