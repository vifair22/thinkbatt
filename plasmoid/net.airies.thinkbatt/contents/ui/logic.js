// SPDX-License-Identifier: GPL-3.0-or-later
//
// Pure helpers for the thinkbatt plasmoid: building the CLI commands it runs,
// and turning the daemon's status payload into display strings. Kept free of
// QML objects so qmltestrunner can exercise every branch (plasmoid/tests).
//
// The plasmoid never talks to the daemon socket itself. It runs the thinkbatt
// CLI through Plasma's executable engine, which hands the command to a shell,
// so every argument that did not come from the user's own configuration is
// single-quoted here.

.pragma library

var MODE_FULL = "full";
var MODE_TIME = "time";
var MODE_DATETIME = "datetime";

function pad2(n) {
    return (n < 10 ? "0" : "") + n;
}

// shellQuote wraps s in single quotes, escaping any embedded single quote, so
// the shell passes it through as one literal argument.
function shellQuote(s) {
    return "'" + String(s).replace(/'/g, "'\\''") + "'";
}

// validPackName accepts kernel power_supply names (BAT0, BAT1, ...). A name is
// only ever taken from the daemon's own status, but it ends up on a command
// line, so anything unexpected is refused rather than quoted and hoped for.
function validPackName(name) {
    return typeof name === "string" && /^[A-Za-z0-9_-]+$/.test(name);
}

// command joins the configured CLI prefix (trusted: the user typed it in the
// plasmoid's settings, and it may carry env vars or a path) with quoted args.
function command(prefix, args) {
    return [prefix].concat(args.map(shellQuote)).join(" ");
}

function statusArgs() {
    return ["status", "--json"];
}

// untilArg renders the --until value the CLI expects for an override mode. A
// bare HH:MM is left for the CLI to resolve to its next occurrence, so the
// plasmoid and the command line mean the same thing by it.
function untilArg(mode, when) {
    if (mode === MODE_FULL) {
        return "full";
    }
    var hm = pad2(when.getHours()) + ":" + pad2(when.getMinutes());
    if (mode === MODE_TIME) {
        return hm;
    }
    if (mode === MODE_DATETIME) {
        return when.getFullYear() + "-" + pad2(when.getMonth() + 1) + "-" + pad2(when.getDate()) + " " + hm;
    }
    throw new Error("unknown override mode " + mode);
}

function chargeArgs(pack, target, mode, when) {
    if (!validPackName(pack)) {
        throw new Error("invalid pack name " + pack);
    }
    var t = target >= 100 ? "full" : String(target);
    return ["charge", "--battery", pack, "--until", untilArg(mode, when), t];
}

function resetArgs(pack) {
    if (!validPackName(pack)) {
        throw new Error("invalid pack name " + pack);
    }
    return ["charge", "reset", "--battery", pack];
}

function calibrateArgs(pack) {
    if (!validPackName(pack)) {
        throw new Error("invalid pack name " + pack);
    }
    return ["calibrate", "--battery", pack, "--now"];
}

function cancelArgs() {
    return ["cancel"];
}

// parseStatus decodes `thinkbatt status --json`. It throws on anything that is
// not the expected shape, so the caller shows an error instead of a blank pane.
function parseStatus(text) {
    var info = JSON.parse(text);
    if (!info || typeof info !== "object") {
        throw new Error("status is not an object");
    }
    if (!info.packs) {
        info.packs = [];
    }
    if (!Array.isArray(info.packs)) {
        throw new Error("status packs is not a list");
    }
    return info;
}

// formatPower renders the signed flow into the pack (positive in).
function formatPower(uw) {
    var w = Math.abs(uw) / 1e6;
    if (uw > 0) {
        return w.toFixed(1) + " W in";
    }
    if (uw < 0) {
        return w.toFixed(1) + " W out";
    }
    return "0.0 W";
}

function formatVoltage(uv) {
    return (uv / 1e6).toFixed(2) + " V";
}

function formatWh(uwh) {
    return (uwh / 1e6).toFixed(1) + " Wh";
}

// healthPercent is the learned full-charge capacity as a share of design, or
// -1 when the pack does not report both.
function healthPercent(full, design) {
    if (!(full > 0) || !(design > 0)) {
        return -1;
    }
    return Math.round(full / design * 1000) / 10;
}

function isCharging(pack) {
    return pack.status === "Charging";
}

function canCalibrate(pack) {
    return / calibrate=yes/.test(" " + (pack.caps_summary || ""));
}

// iconName picks the Breeze battery icon for a state of charge, in the same
// ten-point steps Plasma's own battery applet uses.
function iconName(capacity, charging) {
    var step = Math.max(0, Math.min(100, Math.round(capacity / 10) * 10));
    var name = "battery-" + (step < 10 ? "00" : step < 100 ? "0" : "") + step;
    return charging ? name + "-charging" : name;
}

// effectiveBand is the band the pack is held at now: the override's target
// pair while one is active (matching the daemon's target-1/target), otherwise
// the resting band.
function effectiveBand(pack) {
    if (pack.override) {
        return { start: pack.override.target - 1, stop: pack.override.target };
    }
    return { start: pack.band_start, stop: pack.band_stop };
}

var DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];
var MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];

function sameDay(a, b) {
    return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

// describeWhen renders a deadline relative to now: "09:00" today, "09:00
// tomorrow", or "Sat 18 Oct 09:00" further out.
function describeWhen(when, now) {
    var hm = pad2(when.getHours()) + ":" + pad2(when.getMinutes());
    if (sameDay(when, now)) {
        return hm;
    }
    var tomorrow = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1);
    if (sameDay(when, tomorrow)) {
        return hm + " tomorrow";
    }
    return DAYS[when.getDay()] + " " + when.getDate() + " " + MONTHS[when.getMonth()] + " " + hm;
}

function describeOverride(o, now) {
    var head = "Charging to " + o.target + "%";
    if (o.until_full) {
        return head + " until full";
    }
    return head + " until " + describeWhen(new Date(o.until), now);
}

// nextOccurrence is the next time the clock reads h:m, matching how the CLI
// resolves a bare HH:MM: later today, or tomorrow if that time has passed.
function nextOccurrence(h, m, now) {
    var at = new Date(now.getFullYear(), now.getMonth(), now.getDate(), h, m, 0, 0);
    if (at.getTime() <= now.getTime()) {
        at = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, h, m, 0, 0);
    }
    return at;
}

// combine sets a calendar day's clock to h:m.
function combine(day, h, m) {
    return new Date(day.getFullYear(), day.getMonth(), day.getDate(), h, m, 0, 0);
}

function describeLastCalibration(lc) {
    if (!lc) {
        return "never";
    }
    var d = new Date(lc.finished_at);
    var s = d.getFullYear() + "-" + pad2(d.getMonth() + 1) + "-" + pad2(d.getDate()) + ", " + lc.outcome;
    return lc.reason ? s + " (" + lc.reason + ")" : s;
}

// formatDuration renders an estimate to the minute, the precision it has:
// "2h 05m", "12m", or "under a minute".
function formatDuration(seconds) {
    var m = Math.round(seconds / 60);
    if (m < 1) {
        return "under a minute";
    }
    if (m < 60) {
        return m + "m";
    }
    return Math.floor(m / 60) + "h " + pad2(m % 60) + "m";
}

// describeEstimate renders the daemon's time estimate for a pack ("80% in 1h
// 50m", "3h 10m remaining"), or "" when it gave none. The charge estimate runs
// to where charging actually stops, the band's stop or an override's target,
// not to 100%.
function describeEstimate(pack) {
    if (pack.time_to_stop_s > 0) {
        return pack.charge_target + "% in " + formatDuration(pack.time_to_stop_s);
    }
    if (pack.time_to_empty_s > 0) {
        return formatDuration(pack.time_to_empty_s) + " remaining";
    }
    return "";
}

// summary is the one-line tooltip text for a pack.
function summary(pack, now) {
    var parts = [pack.name + " " + pack.capacity + "%", formatPower(pack.power_uw)];
    var eta = describeEstimate(pack);
    if (eta !== "") {
        parts.push(eta);
    }
    if (pack.calibration_running) {
        parts.push("calibrating" + (pack.calibration_phase ? ": " + pack.calibration_phase : ""));
    } else if (pack.override) {
        parts.push(describeOverride(pack.override, now).toLowerCase());
    } else if (pack.has_thresholds) {
        parts.push("band " + pack.band_start + "-" + pack.band_stop + "%");
    }
    return parts.join(", ");
}

// Sleep and screen-lock blocking goes through powerdevil's PolicyAgent on the
// session bus, the interface Plasma's own battery applet uses. An inhibition is
// tied to the D-Bus connection that asked for it, which here is plasmashell's,
// so it lasts exactly as long as the plasmoid does.
var POLICY_SERVICE = "org.kde.Solid.PowerManagement";
var POLICY_PATH = "/org/kde/Solid/PowerManagement/PolicyAgent";
var POLICY_IFACE = "org.kde.Solid.PowerManagement.PolicyAgent";
// PolicyAgent's RequiredPolicy bits: InterruptSession (1, sleep) and
// ChangeScreenSettings (4, dimming and locking). Plasma's manual block asks for
// the same pair.
var INHIBIT_TYPES = 5;
// Who and why the plasmoid's own inhibition is filed under. Other inhibitors
// see these in their own lists, and the plasmoid uses them to tell its own
// entry apart from everyone else's.
var INHIBIT_APP = "thinkbatt";
var INHIBIT_REASON = "Blocked from the thinkbatt tray";

// unwrap takes the plain value out of the {value: x} wrappers the Plasma D-Bus
// QML module puts around each field of a decoded struct.
function unwrap(v) {
    return v !== null && typeof v === "object" && !isList(v) && "value" in v ? v.value : v;
}

// isList accepts a JS array and the sequence objects the D-Bus module returns
// for D-Bus arrays and structs, which index and have a length but fail
// Array.isArray.
function isList(v) {
    return v !== null && typeof v === "object" && typeof v.length === "number";
}

function toList(v) {
    return isList(v) ? Array.prototype.slice.call(v) : null;
}

// parseInhibitions decodes PolicyAgent's RequestedInhibitions, an a(ssssu) of
// (what, who, why, mode, flags). Requested lists every inhibition including
// the ones the user has blocked; flags is 3 for one in force and 0 for a
// blocked one, so any set bit counts as allowed. Requests that share who and
// why (one app asking for sleep and idle separately) are merged, since
// SetInhibitionAllowed blocks or allows them together.
function parseInhibitions(value) {
    var list = toList(unwrap(value));
    if (!list) {
        return [];
    }
    var byKey = {};
    var out = [];
    for (var i = 0; i < list.length; i++) {
        var f = toList(unwrap(list[i]));
        if (!f || f.length < 5) {
            continue;
        }
        var what = String(unwrap(f[0]));
        var who = String(unwrap(f[1]));
        var why = String(unwrap(f[2]));
        var allowed = Number(unwrap(f[4])) !== 0;
        var key = who + "\n" + why;
        var e = byKey[key];
        if (!e) {
            e = { who: who, why: why, what: [], allowed: allowed, own: who === INHIBIT_APP && why === INHIBIT_REASON };
            byKey[key] = e;
            out.push(e);
        }
        e.allowed = e.allowed && allowed;
        var parts = what.split(":");
        for (var j = 0; j < parts.length; j++) {
            if (parts[j] !== "" && e.what.indexOf(parts[j]) < 0) {
                e.what.push(parts[j]);
            }
        }
    }
    return out;
}

var WHAT_NAMES = { sleep: "sleep", idle: "screen locking", "handle-lid-switch": "lid close", shutdown: "shutdown" };

// describeBlocked names what an inhibition holds off: "sleep and screen
// locking", from logind-style what tokens.
function describeBlocked(what) {
    var names = [];
    for (var i = 0; i < what.length; i++) {
        var n = WHAT_NAMES[what[i]] || what[i];
        if (names.indexOf(n) < 0) {
            names.push(n);
        }
    }
    if (names.length <= 1) {
        return names.join("");
    }
    return names.slice(0, -1).join(", ") + " and " + names[names.length - 1];
}

// policyCall builds the message for a PolicyAgent method; args must already be
// wrapped in the D-Bus module's typed values.
function policyCall(member, args, signature) {
    return { service: POLICY_SERVICE, path: POLICY_PATH, iface: POLICY_IFACE, member: member, arguments: args, signature: signature };
}

// Calibrations ended by the user's own hand (a cancel, or an override that
// displaced it) are not news to them, so they raise no notification.
var QUIET_CALIBRATION_REASONS = ["cancelled by user", "cancelled for a charge override"];

function overrideKey(o) {
    return o ? o.target + "|" + (o.until_full ? "full" : o.until) + "|" + o.set_at : "";
}

// diffEvents compares the packs in a status against what the plasmoid saw
// last, and returns the events worth a notification plus the record to keep.
// seen maps a pack name to {override, calibration}: the override's key and the
// last calibration's finish time. It is persisted, so an override that ended
// while the plasmoid was not running (suspended, logged out) is still reported
// on the first status after. A pack seen for the first time only seeds the
// record, and a pack missing from this status keeps its entry for its return.
// quiet names packs whose override the user just ended from the popup.
function diffEvents(seen, packs, quiet) {
    var next = {};
    var events = [];
    var name;
    for (name in seen) {
        next[name] = seen[name];
    }
    for (var i = 0; i < packs.length; i++) {
        var p = packs[i];
        var lc = p.last_calibration;
        var rec = { override: overrideKey(p.override), calibration: lc ? lc.finished_at : "" };
        var prev = seen[p.name];
        next[p.name] = rec;
        if (!prev) {
            continue;
        }
        if (prev.override !== "" && rec.override === "" && !(quiet && quiet[p.name])) {
            events.push({ kind: "override-ended", pack: p.name, target: Number(prev.override.split("|")[0]),
                          bandStart: p.band_start, bandStop: p.band_stop });
        }
        if (lc && rec.calibration !== prev.calibration && QUIET_CALIBRATION_REASONS.indexOf(lc.reason) < 0) {
            events.push({ kind: "calibration-ended", pack: p.name, outcome: lc.outcome, reason: lc.reason || "",
                          fullBefore: lc.energy_full_before_uwh || 0, fullAfter: lc.energy_full_after_uwh || 0 });
        }
    }
    return { events: events, seen: next };
}

// parseSeen reads the persisted record, treating anything unreadable as empty
// (which only means the next status seeds it again).
function parseSeen(text) {
    try {
        var v = JSON.parse(text || "{}");
        return v && typeof v === "object" && !Array.isArray(v) ? v : {};
    } catch (e) {
        return {};
    }
}

// notificationFor turns a diffEvents event into the notification's title,
// body and icon.
function notificationFor(ev) {
    if (ev.kind === "override-ended") {
        return {
            title: "Charge override ended",
            text: ev.pack + " is back to its " + ev.bandStart + "-" + ev.bandStop + "% resting band after charging to " + ev.target + "%.",
            icon: "battery-good",
        };
    }
    if (ev.outcome === "ok") {
        var text = ev.pack + " finished its learning cycle.";
        if (ev.fullBefore > 0 && ev.fullAfter > 0) {
            text += " Gauge full " + formatWh(ev.fullBefore) + " -> " + formatWh(ev.fullAfter) + ".";
        }
        return { title: "Calibration finished", text: text, icon: "battery-full" };
    }
    return {
        title: ev.outcome === "failed" ? "Calibration failed" : "Calibration stopped",
        text: ev.pack + ": " + (ev.reason || ev.outcome) + ". The pack is back in its resting band.",
        icon: "dialog-warning",
    };
}
