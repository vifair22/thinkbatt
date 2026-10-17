// SPDX-License-Identifier: GPL-3.0-or-later
//
// Unit tests for the plasmoid's logic.js, run by qmltestrunner (make
// test-plasmoid). Times are built with local-time constructors so the tests do
// not depend on the machine's timezone.

import QtQuick
import QtTest
import "../net.airies.thinkbatt/contents/ui/logic.js" as Logic

TestCase {
    name: "Logic"

    function test_shellQuote() {
        compare(Logic.shellQuote("BAT0"), "'BAT0'");
        compare(Logic.shellQuote("2026-10-18 09:00"), "'2026-10-18 09:00'");
        compare(Logic.shellQuote("it's"), "'it'\\''s'");
        compare(Logic.shellQuote(90), "'90'");
    }

    function test_validPackName() {
        verify(Logic.validPackName("BAT0"));
        verify(Logic.validPackName("BAT1"));
        verify(!Logic.validPackName("BAT0; rm -rf ~"));
        verify(!Logic.validPackName(""));
        verify(!Logic.validPackName(undefined));
    }

    function test_command() {
        compare(Logic.command("thinkbatt", Logic.statusArgs()), "thinkbatt 'status' '--json'");
        compare(Logic.command("env THINKBATT_SOCKET=/run/x thinkbatt", Logic.cancelArgs()),
                "env THINKBATT_SOCKET=/run/x thinkbatt 'cancel'");
    }

    function test_chargeArgs() {
        var when = new Date(2026, 9, 18, 9, 5);
        compare(Logic.chargeArgs("BAT0", 100, Logic.MODE_FULL, when),
                ["charge", "--battery", "BAT0", "--until", "full", "full"]);
        compare(Logic.chargeArgs("BAT0", 90, Logic.MODE_TIME, when),
                ["charge", "--battery", "BAT0", "--until", "09:05", "90"]);
        compare(Logic.chargeArgs("BAT1", 100, Logic.MODE_DATETIME, when),
                ["charge", "--battery", "BAT1", "--until", "2026-10-18 09:05", "full"]);
    }

    function test_argsRejectBadInput() {
        var bad = [
            function () { Logic.chargeArgs("BAT0 x", 100, Logic.MODE_FULL, new Date()); },
            function () { Logic.chargeArgs("BAT0", 100, "sometime", new Date()); },
            function () { Logic.resetArgs("$(x)"); },
            function () { Logic.calibrateArgs("`x`"); },
        ];
        for (var i = 0; i < bad.length; i++) {
            var threw = false;
            try {
                bad[i]();
            } catch (e) {
                threw = true;
            }
            verify(threw, "case " + i + " should throw");
        }
    }

    function test_resetAndCalibrateArgs() {
        compare(Logic.resetArgs("BAT0"), ["charge", "reset", "--battery", "BAT0"]);
        compare(Logic.calibrateArgs("BAT0"), ["calibrate", "--battery", "BAT0", "--now"]);
    }

    function test_parseStatus() {
        var info = Logic.parseStatus('{"packs":[{"name":"BAT0","capacity":57}]}');
        compare(info.packs.length, 1);
        compare(info.packs[0].capacity, 57);
        compare(Logic.parseStatus("{}").packs.length, 0);
        var bad = ["not json", "3", '{"packs":"BAT0"}', "null"];
        for (var i = 0; i < bad.length; i++) {
            var threw = false;
            try {
                Logic.parseStatus(bad[i]);
            } catch (e) {
                threw = true;
            }
            verify(threw, bad[i] + " should be rejected");
        }
    }

    function test_formatting() {
        compare(Logic.formatPower(6800000), "6.8 W in");
        compare(Logic.formatPower(-6609915), "6.6 W out");
        compare(Logic.formatPower(0), "0.0 W");
        compare(Logic.formatVoltage(11299000), "11.30 V");
        compare(Logic.formatWh(56770560), "56.8 Wh");
    }

    function test_healthPercent() {
        compare(Logic.healthPercent(56770560, 57000960), 99.6);
        compare(Logic.healthPercent(0, 57000960), -1);
        compare(Logic.healthPercent(56770560, 0), -1);
    }

    function test_iconName() {
        compare(Logic.iconName(57, false), "battery-060");
        compare(Logic.iconName(54, true), "battery-050-charging");
        compare(Logic.iconName(100, false), "battery-100");
        compare(Logic.iconName(3, false), "battery-000");
        compare(Logic.iconName(8, false), "battery-010");
    }

    function test_caps() {
        verify(Logic.canCalibrate({ caps_summary: "thresholds=yes band=yes calibrate=yes" }));
        verify(!Logic.canCalibrate({ caps_summary: "thresholds=yes band=yes calibrate=no" }));
        verify(!Logic.canCalibrate({}));
        verify(Logic.isCharging({ status: "Charging" }));
        verify(!Logic.isCharging({ status: "Not charging" }));
    }

    function test_effectiveBand() {
        compare(Logic.effectiveBand({ band_start: 75, band_stop: 80 }), { start: 75, stop: 80 });
        compare(Logic.effectiveBand({ band_start: 75, band_stop: 80, override: { target: 100 } }),
                { start: 99, stop: 100 });
    }

    function test_describeWhen() {
        var now = new Date(2026, 9, 16, 15, 0);
        compare(Logic.describeWhen(new Date(2026, 9, 16, 18, 30), now), "18:30");
        compare(Logic.describeWhen(new Date(2026, 9, 17, 9, 0), now), "09:00 tomorrow");
        compare(Logic.describeWhen(new Date(2026, 9, 18, 9, 0), now), "Sun 18 Oct 09:00");
    }

    function test_describeOverride() {
        var now = new Date(2026, 9, 16, 15, 0);
        compare(Logic.describeOverride({ target: 100, until_full: true }, now), "Charging to 100% until full");
        var until = new Date(2026, 9, 17, 9, 0).toISOString();
        compare(Logic.describeOverride({ target: 90, until: until }, now), "Charging to 90% until 09:00 tomorrow");
    }

    function test_nextOccurrence() {
        var now = new Date(2026, 9, 16, 15, 0);
        compare(Logic.nextOccurrence(18, 30, now).getTime(), new Date(2026, 9, 16, 18, 30).getTime());
        compare(Logic.nextOccurrence(9, 0, now).getTime(), new Date(2026, 9, 17, 9, 0).getTime());
        compare(Logic.nextOccurrence(15, 0, now).getTime(), new Date(2026, 9, 17, 15, 0).getTime());
        // Month rollover.
        var eom = new Date(2026, 9, 31, 23, 0);
        compare(Logic.nextOccurrence(8, 0, eom).getTime(), new Date(2026, 10, 1, 8, 0).getTime());
    }

    function test_combine() {
        compare(Logic.combine(new Date(2026, 9, 18, 13, 47), 9, 0).getTime(), new Date(2026, 9, 18, 9, 0).getTime());
    }

    function test_describeLastCalibration() {
        compare(Logic.describeLastCalibration(null), "never");
        var lc = { finished_at: new Date(2026, 9, 11, 23, 0).toISOString(), outcome: "ok", reason: "learning cycle complete" };
        compare(Logic.describeLastCalibration(lc), "2026-10-11, ok (learning cycle complete)");
        lc.reason = "";
        compare(Logic.describeLastCalibration(lc), "2026-10-11, ok");
    }

    function test_summary() {
        var now = new Date(2026, 9, 16, 15, 0);
        var p = { name: "BAT0", capacity: 57, power_uw: -6600000, has_thresholds: true, band_start: 75, band_stop: 80 };
        compare(Logic.summary(p, now), "BAT0 57%, 6.6 W out, band 75-80%");
        p.override = { target: 100, until_full: true };
        compare(Logic.summary(p, now), "BAT0 57%, 6.6 W out, charging to 100% until full");
        p.calibration_running = true;
        p.calibration_phase = "discharging";
        compare(Logic.summary(p, now), "BAT0 57%, 6.6 W out, calibrating: discharging");
        p.calibration_phase = "";
        compare(Logic.summary(p, now), "BAT0 57%, 6.6 W out, calibrating");
        var bare = { name: "BAT0", capacity: 57, power_uw: 0, has_thresholds: false };
        compare(Logic.summary(bare, now), "BAT0 57%, 0.0 W");
    }
    function test_estimates() {
        compare(Logic.formatDuration(20), "under a minute");
        compare(Logic.formatDuration(720), "12m");
        compare(Logic.formatDuration(6600), "1h 50m");
        compare(Logic.formatDuration(3600 * 3 + 300), "3h 05m");
        compare(Logic.describeEstimate({ charge_target: 80, time_to_stop_s: 6600 }), "80% in 1h 50m");
        compare(Logic.describeEstimate({ time_to_empty_s: 11400 }), "3h 10m remaining");
        compare(Logic.describeEstimate({ charge_target: 80 }), "");
        compare(Logic.summary({ name: "BAT0", capacity: 45, power_uw: 11200000, has_thresholds: true,
                                band_start: 75, band_stop: 80, charge_target: 80, time_to_stop_s: 6600 }, new Date()),
                "BAT0 45%, 11.2 W in, 80% in 1h 50m, band 75-80%");
    }

    // The shape the Plasma D-Bus module hands back for a(ssssu): each field
    // wrapped in {value}.
    function wrapped(rows) {
        return rows.map((r) => r.map((v) => ({ value: v })));
    }

    function test_parseInhibitions() {
        var got = Logic.parseInhibitions(wrapped([
            ["sleep", "Running Script", "sleep", "block", 3],
            ["idle", "Running Script", "sleep", "block", 3],
            ["sleep:idle", "probe", "probe why", "block", 0],
            ["sleep:idle", Logic.INHIBIT_APP, Logic.INHIBIT_REASON, "block", 3],
            ["short"],
        ]));
        compare(got.length, 3);
        compare(got[0].who, "Running Script");
        compare(got[0].what, ["sleep", "idle"]);
        verify(got[0].allowed);
        verify(!got[0].own);
        verify(!got[1].allowed, "flags 0 is a blocked request");
        verify(got[2].own);
        // The module's sequences index and have a length but are not arrays.
        var row = { length: 5, 0: { value: "sleep" }, 1: { value: "seq" }, 2: { value: "why" }, 3: { value: "block" }, 4: { value: 3 } };
        var seq = Logic.parseInhibitions({ length: 1, 0: row });
        compare(seq.length, 1);
        compare(seq[0].who, "seq");
        compare(Logic.parseInhibitions({ value: [] }), []);
        compare(Logic.parseInhibitions(undefined), []);
        compare(Logic.unwrap({ value: 22 }), 22);
        compare(Logic.unwrap(22), 22);
    }

    function test_describeBlocked() {
        compare(Logic.describeBlocked(["sleep", "idle"]), "sleep and screen locking");
        compare(Logic.describeBlocked(["sleep"]), "sleep");
        compare(Logic.describeBlocked(["sleep", "idle", "handle-lid-switch"]), "sleep, screen locking and lid close");
        compare(Logic.describeBlocked(["sleep", "sleep"]), "sleep");
        compare(Logic.describeBlocked(["weird"]), "weird");
    }

    function test_policyCall() {
        var m = Logic.policyCall("ReleaseInhibition", [7], "(u)");
        compare(m.service, "org.kde.Solid.PowerManagement");
        compare(m.member, "ReleaseInhibition");
        compare(m.signature, "(u)");
    }
}
