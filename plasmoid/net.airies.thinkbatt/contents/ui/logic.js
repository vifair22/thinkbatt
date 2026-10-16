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

// summary is the one-line tooltip text for a pack.
function summary(pack, now) {
    var parts = [pack.name + " " + pack.capacity + "%", formatPower(pack.power_uw)];
    if (pack.calibration_running) {
        parts.push("calibrating" + (pack.calibration_phase ? ": " + pack.calibration_phase : ""));
    } else if (pack.override) {
        parts.push(describeOverride(pack.override, now).toLowerCase());
    } else if (pack.has_thresholds) {
        parts.push("band " + pack.band_start + "-" + pack.band_stop + "%");
    }
    return parts.join(", ");
}
