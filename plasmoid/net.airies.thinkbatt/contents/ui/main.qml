// SPDX-License-Identifier: GPL-3.0-or-later
//
// thinkbatt plasmoid: a system-tray front end for thinkbattd. It polls
// `thinkbatt status --json` (every 3 s while the popup is open, 30 s while
// closed) through Plasma's executable engine and runs the same CLI for every
// action, so it needs nothing the command line does not: the user must be able
// to reach the control socket (socket_group in thinkbatt.ini).

pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Layouts
import org.kde.plasma.plasmoid
import org.kde.plasma.core as PlasmaCore
import org.kde.plasma.components as PlasmaComponents3
import org.kde.plasma.extras as PlasmaExtras
import org.kde.plasma.plasma5support as P5Support
import org.kde.kirigami as Kirigami
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

    readonly property string cli: Plasmoid.configuration.command || "thinkbatt"
    readonly property var primary: packs.length > 0 ? packs[0] : null
    readonly property bool anyOverride: packs.some((p) => !!p.override)
    readonly property bool anyCalibrating: packs.some((p) => p.calibration_running)

    Plasmoid.icon: primary ? Logic.iconName(primary.capacity, Logic.isCharging(primary)) : "battery-missing"
    Plasmoid.status: PlasmaCore.Types.ActiveStatus
    toolTipMainText: i18n("thinkbatt")
    toolTipSubText: error !== "" ? error : packs.map((p) => Logic.summary(p, now)).join("\n")

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
            } catch (e) {
                error = i18n("Unexpected status from thinkbatt: %1", e.message);
            }
        });
    }

    function act(args) {
        actionError = "";
        busy = true;
        const started = exec.run(Logic.command(cli, args), (code, out, err) => {
            busy = false;
            if (code !== 0) {
                actionError = cleanError(code, out, err);
            }
            refresh();
        });
        if (!started) {
            busy = false;
        }
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
        // cannot be reached.
        Kirigami.Icon {
            readonly property string badge: root.error !== "" ? "dialog-warning"
                : root.anyCalibrating ? "view-refresh"
                : root.anyOverride ? "go-up"
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
                                         body.implicitHeight + header.implicitHeight + Kirigami.Units.gridUnit * 2)

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
            PlasmaComponents3.ScrollBar.horizontal.policy: PlasmaComponents3.ScrollBar.AlwaysOff

            ColumnLayout {
                id: body
                // Explicit margins: plasmawindowed gives none, and the tray
                // popup's own are thin against the scrollbar. The width is
                // taken from the view with a fixed gutter for the vertical
                // scrollbar, not from the width left once it shows: wrapped
                // text reflows with the width, which changes the height,
                // which toggles the scrollbar, a binding loop.
                x: Kirigami.Units.largeSpacing
                width: scroll.width - 2 * Kirigami.Units.largeSpacing - Kirigami.Units.gridUnit
                spacing: Kirigami.Units.largeSpacing

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
            }
        }
    }
}
