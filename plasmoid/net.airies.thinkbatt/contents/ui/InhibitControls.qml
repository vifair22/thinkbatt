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

    Kirigami.Heading {
        level: 4
        text: i18n("Sleep and screen locking")
    }

    PlasmaComponents3.Switch {
        Layout.fillWidth: true
        text: i18n("Block manually")
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

    // Each app's request: its name, then what it holds off and why, with the
    // button to overrule it.
    Repeater {
        model: inh.others
        delegate: RowLayout {
            id: row
            required property var modelData
            Layout.fillWidth: true
            Layout.topMargin: Kirigami.Units.smallSpacing
            spacing: Kirigami.Units.largeSpacing

            Kirigami.Icon {
                Layout.alignment: Qt.AlignTop
                source: row.modelData.allowed ? "system-suspend-inhibited" : "system-suspend-uninhibited"
                implicitWidth: Kirigami.Units.iconSizes.smallMedium
                implicitHeight: Kirigami.Units.iconSizes.smallMedium
            }
            ColumnLayout {
                Layout.fillWidth: true
                spacing: 0
                PlasmaComponents3.Label {
                    Layout.fillWidth: true
                    elide: Text.ElideRight
                    font.weight: Font.DemiBold
                    opacity: row.modelData.allowed ? 1 : 0.6
                    text: row.modelData.who
                }
                PlasmaComponents3.Label {
                    Layout.fillWidth: true
                    wrapMode: Text.Wrap
                    font: Kirigami.Theme.smallFont
                    opacity: 0.7
                    text: row.modelData.allowed
                        ? i18n("Blocking %1: %2", Logic.describeBlocked(row.modelData.what), row.modelData.why)
                        : i18n("Not allowed to block %1: %2", Logic.describeBlocked(row.modelData.what), row.modelData.why)
                }
            }
            PlasmaComponents3.Button {
                Layout.alignment: Qt.AlignVCenter
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
