// SPDX-License-Identifier: GPL-3.0-or-later
//
// thinkbatt plasmoid: a system-tray front end for thinkbattd. It polls
// `thinkbatt status --json` (every 3 s while the popup is open, 30 s while
// closed) through Plasma's executable engine and runs the same CLI for every
// action, so it needs nothing the command line does not: the user must be able
// to reach the control socket (socket_group in thinkbatt.ini).
//
// It also takes over the sleep blocking of Plasma's own battery applet, so that
// one can be hidden: those calls go to powerdevil on the session bus. And it
// notifies when an override or a calibration ends, comparing each status
// against a record of the last one kept in its configuration, so an end that
// happened while it was not running is reported when it next sees a status.

pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Layouts
import org.kde.plasma.plasmoid
import org.kde.plasma.core as PlasmaCore
import org.kde.plasma.components as PlasmaComponents3
import org.kde.plasma.extras as PlasmaExtras
import org.kde.plasma.plasma5support as P5Support
import org.kde.kirigami as Kirigami
import org.kde.notification
import org.kde.plasma.workspace.dbus as DBus
import "logic.js" as Logic

PlasmoidItem {
    id: root

    property var packs: []
    // Names drive the Repeater; it only rebuilds cards when packs come or go,
    // so a refresh does not wipe a half-filled override form.
    property var packNames: []
    property string error: ""
    property string actionError: ""
    property bool busy: false
    property date now: new Date()

    // Sleep blocking. The cookie is the plasmoid's own inhibition; it lives on
    // plasmashell's D-Bus connection, so it and this property end together.
    property var inhibitions: []
    property var inhibitCookie: null
    readonly property bool blocking: inhibitCookie !== null
    // What the switch last asked for, and whether an AddInhibition is still
    // in flight: switching off before its reply must not leave a block behind.
    property bool inhibitWanted: false
    property bool inhibitPending: false

    // The notification record (Logic.diffEvents), and packs whose override
    // the user just ended from the popup, which is not news to them.
    property var seen: Logic.parseSeen(Plasmoid.configuration.seen)
    property var quietOverride: ({})

    readonly property string cli: Plasmoid.configuration.command || "thinkbatt"
    readonly property var primary: packs.length > 0 ? packs[0] : null
    readonly property bool anyOverride: packs.some((p) => !!p.override)
    readonly property bool anyCalibrating: packs.some((p) => p.calibration_running)

    Plasmoid.icon: primary ? Logic.iconName(primary.capacity, Logic.isCharging(primary)) : "battery-missing"
    Plasmoid.status: PlasmaCore.Types.ActiveStatus
    toolTipMainText: i18n("thinkbatt")
    toolTipSubText: (error !== "" ? error : packs.map((p) => Logic.summary(p, now)).join("\n"))
        + (blocking ? "\n" + i18n("Sleep and screen locking blocked") : "")

    function packByName(name) {
        for (let i = 0; i < packs.length; i++) {
            if (packs[i].name === name) {
                return packs[i];
            }
        }
        return null;
    }

    function cleanError(code, out, err) {
        const msg = ((err || "") + (out || "")).trim().replace(/^thinkbatt: /, "");
        return msg !== "" ? msg : i18n("thinkbatt exited with status %1", code);
    }

    function refresh() {
        exec.run(Logic.command(cli, Logic.statusArgs()), (code, out, err) => {
            now = new Date();
            if (code !== 0) {
                error = cleanError(code, out, err);
                return;
            }
            try {
                const next = Logic.parseStatus(out).packs;
                packs = next;
                const names = next.map((p) => p.name);
                if (names.join(",") !== packNames.join(",")) {
                    packNames = names;
                }
                error = "";
                noteEvents(next);
            } catch (e) {
                error = i18n("Unexpected status from thinkbatt: %1", e.message);
            }
        });
        refreshInhibitions();
    }

    function noteEvents(next) {
        const d = Logic.diffEvents(seen, next, quietOverride);
        for (const ev of d.events) {
            const n = Logic.notificationFor(ev);
            const obj = notifier.createObject(root, { title: n.title, text: n.text, iconName: n.icon });
            obj.closed.connect(() => obj.destroy());
            obj.sendEvent();
        }
        const quiet = {};
        for (const name in quietOverride) {
            const p = packByName(name);
            if (p && p.override) {
                quiet[name] = true;
            }
        }
        quietOverride = quiet;
        const text = JSON.stringify(d.seen);
        if (text !== JSON.stringify(seen)) {
            seen = d.seen;
            Plasmoid.configuration.seen = text;
        }
    }

    // dbusCall runs a PolicyAgent method and hands its unwrapped reply to
    // onReply. A failure lands in the popup's action error, not silently, and
    // then onError runs, if given.
    function dbusCall(member, args, signature, onReply, onError) {
        const reply = DBus.SessionBus.asyncCall(Logic.policyCall(member, args, signature));
        reply.finished.connect(() => {
            if (reply.isError) {
                actionError = i18n("Power management: %1", reply.error.message());
                if (onError) {
                    onError();
                }
            } else if (onReply) {
                onReply(Logic.unwrap(reply.value));
            }
        });
    }

    function refreshInhibitions() {
        if (!powerdevil.registered) {
            inhibitions = [];
            return;
        }
        const reply = DBus.SessionBus.asyncCall({
            service: Logic.POLICY_SERVICE,
            path: Logic.POLICY_PATH,
            iface: "org.freedesktop.DBus.Properties",
            member: "Get",
            arguments: [new DBus.string(Logic.POLICY_IFACE), new DBus.string("RequestedInhibitions")],
            signature: "(ss)",
        });
        reply.finished.connect(() => {
            if (!reply.isError) {
                inhibitions = Logic.parseInhibitions(reply.value);
            }
        });
    }

    function setBlocking(on) {
        actionError = "";
        inhibitWanted = on;
        if (on && inhibitCookie === null && !inhibitPending) {
            inhibitPending = true;
            dbusCall("AddInhibition", [new DBus.uint32(Logic.INHIBIT_TYPES), new DBus.string(Logic.INHIBIT_APP),
                                       new DBus.string(Logic.INHIBIT_REASON)], "(uss)",
                     (cookie) => {
                         inhibitPending = false;
                         if (inhibitWanted) {
                             inhibitCookie = cookie;
                         } else {
                             release(cookie);
                         }
                     },
                     () => { inhibitPending = false; });
        } else if (!on && inhibitCookie !== null) {
            const cookie = inhibitCookie;
            inhibitCookie = null;
            release(cookie);
        }
        listSettle.restart();
    }

    function release(cookie) {
        dbusCall("ReleaseInhibition", [new DBus.uint32(cookie)], "(u)", null);
    }

    function setAllowed(who, why, allowed) {
        actionError = "";
        dbusCall("SetInhibitionAllowed", [new DBus.string(who), new DBus.string(why), new DBus.bool(allowed)], "(ssb)",
                 () => refreshInhibitions());
    }

    function act(args) {
        actionError = "";
        busy = true;
        const resetPack = args[0] === "charge" && args[1] === "reset" ? args[3] : "";
        if (resetPack !== "") {
            setQuiet(resetPack, true);
        }
        const started = exec.run(Logic.command(cli, args), (code, out, err) => {
            busy = false;
            if (code !== 0) {
                actionError = cleanError(code, out, err);
                // The override is still on, so its real end is news again.
                if (resetPack !== "") {
                    setQuiet(resetPack, false);
                }
            }
            refresh();
        });
        if (!started) {
            busy = false;
            if (resetPack !== "") {
                setQuiet(resetPack, false);
            }
        }
    }

    function setQuiet(name, on) {
        const quiet = Object.assign({}, quietOverride);
        if (on) {
            quiet[name] = true;
        } else {
            delete quiet[name];
        }
        quietOverride = quiet;
    }

    P5Support.DataSource {
        id: exec
        engine: "executable"
        connectedSources: []

        // Pending callbacks by command line. The engine names a source by its
        // command, so a second identical command while one is in flight would
        // share its result; run refuses it instead.
        property var handlers: ({})

        function run(cmd, handler) {
            if (handlers[cmd] !== undefined) {
                return false;
            }
            handlers[cmd] = handler;
            connectSource(cmd);
            return true;
        }

        onNewData: (source, data) => {
            const handler = handlers[source];
            delete handlers[source];
            disconnectSource(source);
            if (handler) {
                handler(data["exit code"], data["stdout"], data["stderr"]);
            }
        }
    }

    DBus.DBusServiceWatcher {
        id: powerdevil
        busType: DBus.BusType.Session
        watchedService: Logic.POLICY_SERVICE
        onRegisteredChanged: root.refreshInhibitions()
    }

    // powerdevil lists a new inhibition only after holding it for a few
    // seconds (it ignores ones that come and go), so the list is read again
    // once that has passed rather than straight after a change.
    Timer {
        id: listSettle
        interval: 6000
        onTriggered: root.refreshInhibitions()
    }

    Component {
        id: notifier
        Notification {
            componentName: "plasma_workspace"
            eventId: "notification"
            autoDelete: false
        }
    }

    Timer {
        interval: root.expanded ? 3000 : 30000
        running: true
        repeat: true
        triggeredOnStart: true
        onTriggered: root.refresh()
    }

    onExpandedChanged: (isExpanded) => {
        if (isExpanded) {
            refresh();
        }
    }

    compactRepresentation: MouseArea {
        id: compact

        property bool wasExpanded: false

        hoverEnabled: true
        acceptedButtons: Qt.LeftButton
        onPressed: wasExpanded = root.expanded
        onClicked: root.expanded = !wasExpanded

        Kirigami.Icon {
            anchors.fill: parent
            source: Plasmoid.icon
            active: compact.containsMouse
        }

        // A badge says the pack is not just sitting in its band: an override
        // is holding it higher, a calibration is cycling it, or the daemon
        // cannot be reached; or that sleep is blocked from the popup.
        Kirigami.Icon {
            readonly property string badge: root.error !== "" ? "dialog-warning"
                : root.anyCalibrating ? "view-refresh"
                : root.anyOverride ? "go-up"
                : root.blocking ? "system-suspend-inhibited"
                : ""
            visible: badge !== ""
            source: badge
            anchors.right: parent.right
            anchors.bottom: parent.bottom
            width: Math.round(parent.width * 0.5)
            height: width
        }
    }

    fullRepresentation: PlasmaExtras.Representation {
        Layout.minimumWidth: Kirigami.Units.gridUnit * 20
        Layout.preferredWidth: Kirigami.Units.gridUnit * 22
        Layout.minimumHeight: Kirigami.Units.gridUnit * 12
        Layout.preferredHeight: Math.min(Kirigami.Units.gridUnit * 36,
                                         scroll.contentHeight + header.implicitHeight)

        header: PlasmaExtras.PlasmoidHeading {
            RowLayout {
                anchors.fill: parent
                anchors.leftMargin: Kirigami.Units.largeSpacing
                Kirigami.Heading {
                    Layout.fillWidth: true
                    level: 2
                    text: i18n("Battery")
                }
                PlasmaComponents3.BusyIndicator {
                    visible: root.busy
                    implicitWidth: Kirigami.Units.iconSizes.small
                    implicitHeight: Kirigami.Units.iconSizes.small
                }
                PlasmaComponents3.ToolButton {
                    icon.name: "view-refresh"
                    onClicked: root.refresh()
                    PlasmaComponents3.ToolTip.text: i18n("Refresh")
                    PlasmaComponents3.ToolTip.visible: hovered
                }
            }
        }

        PlasmaComponents3.ScrollView {
            id: scroll
            anchors.fill: parent
            contentWidth: availableWidth
            contentHeight: body.implicitHeight + 2 * Kirigami.Units.largeSpacing
            PlasmaComponents3.ScrollBar.horizontal.policy: PlasmaComponents3.ScrollBar.AlwaysOff

            ColumnLayout {
                id: body
                // Explicit margins: plasmawindowed gives none, and the tray
                // popup's own are thin against the scrollbar. They are even,
                // a grid unit each side, wide enough that the vertical
                // scrollbar sits inside the right one when it shows. The
                // width is taken from the view, not from the width left once
                // the scrollbar shows: wrapped text reflows with the width,
                // which changes the height, which toggles the scrollbar, a
                // binding loop.
                x: Kirigami.Units.gridUnit
                y: Kirigami.Units.largeSpacing
                width: scroll.width - 2 * Kirigami.Units.gridUnit
                spacing: Kirigami.Units.gridUnit

                Kirigami.InlineMessage {
                    Layout.fillWidth: true
                    visible: root.error !== ""
                    type: Kirigami.MessageType.Error
                    text: i18n("Cannot reach thinkbattd: %1", root.error)
                }

                Kirigami.InlineMessage {
                    Layout.fillWidth: true
                    visible: root.actionError !== ""
                    type: Kirigami.MessageType.Warning
                    text: root.actionError
                    showCloseButton: true
                    onVisibleChanged: if (!visible) root.actionError = ""
                }

                PlasmaExtras.PlaceholderMessage {
                    Layout.fillWidth: true
                    visible: root.error === "" && root.packNames.length === 0
                    iconName: "battery-missing"
                    text: i18n("No battery packs present")
                }

                Repeater {
                    model: root.packNames
                    delegate: PackCard {
                        required property string modelData
                        Layout.fillWidth: true
                        pack: root.packByName(modelData) || ({ name: modelData })
                        now: root.now
                        busy: root.busy
                        onAction: (args) => root.act(args)
                    }
                }

                // Machine-wide rather than per pack, so after the packs, as
                // in Plasma's own applet.
                InhibitControls {
                    Layout.fillWidth: true
                    inhibitions: root.inhibitions
                    blocking: root.blocking
                    available: powerdevil.registered
                    busy: root.inhibitPending
                    onBlockingRequested: (on) => root.setBlocking(on)
                    onAllowRequested: (who, why, allowed) => root.setAllowed(who, why, allowed)
                }
            }
        }
    }
}
