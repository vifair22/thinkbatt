// SPDX-License-Identifier: GPL-3.0-or-later
//
// Sleep and screen-lock blocking, the part of Plasma's battery applet that
// thinkbatt replaces: a switch for a manual block of its own, and every other
// app's inhibition with a toggle to block or allow it. The D-Bus calls are
// main.qml's; this only shows the state and emits what the user asked for.

pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Layouts
import org.kde.kirigami as Kirigami
import org.kde.plasma.components as PlasmaComponents3
import "logic.js" as Logic

ColumnLayout {
    id: inh

    required property var inhibitions
    required property bool blocking
    property bool available: true
    property bool busy: false
    signal blockingRequested(bool on)
    signal allowRequested(string who, string why, bool allowed)

    readonly property var others: inhibitions.filter((e) => !e.own)

    spacing: Kirigami.Units.smallSpacing

    PlasmaComponents3.Switch {
        id: blockSwitch
        Layout.fillWidth: true
        text: i18n("Block sleep and screen locking")
        checked: inh.blocking
        enabled: inh.available && !inh.busy
        onToggled: {
            inh.blockingRequested(checked);
            // The switch follows the inhibition, not the click: if the call
            // fails it falls back to where it was.
            checked = Qt.binding(() => inh.blocking);
        }
    }

    PlasmaComponents3.Label {
        visible: !inh.available
        Layout.fillWidth: true
        wrapMode: Text.Wrap
        opacity: 0.7
        text: i18n("Power management is not running, so sleep cannot be blocked from here.")
    }

    Repeater {
        model: inh.others
        delegate: RowLayout {
            id: row
            required property var modelData
            Layout.fillWidth: true
            Kirigami.Icon {
                source: row.modelData.allowed ? "system-suspend-inhibited" : "system-suspend-uninhibited"
                implicitWidth: Kirigami.Units.iconSizes.small
                implicitHeight: Kirigami.Units.iconSizes.small
            }
            PlasmaComponents3.Label {
                Layout.fillWidth: true
                wrapMode: Text.Wrap
                opacity: row.modelData.allowed ? 1 : 0.6
                text: row.modelData.allowed
                    ? i18n("%1 is blocking %2: %3", row.modelData.who, Logic.describeBlocked(row.modelData.what), row.modelData.why)
                    : i18n("%1 wanted to block %2 (not allowed): %3", row.modelData.who, Logic.describeBlocked(row.modelData.what), row.modelData.why)
            }
            PlasmaComponents3.ToolButton {
                text: row.modelData.allowed ? i18n("Block") : i18n("Allow")
                enabled: !inh.busy
                onClicked: inh.allowRequested(row.modelData.who, row.modelData.why, !row.modelData.allowed)
                PlasmaComponents3.ToolTip.text: row.modelData.allowed
                    ? i18n("Ignore this request and let the machine sleep and lock as usual")
                    : i18n("Let this request hold off sleep and locking again")
                PlasmaComponents3.ToolTip.visible: hovered
            }
        }
    }
}
