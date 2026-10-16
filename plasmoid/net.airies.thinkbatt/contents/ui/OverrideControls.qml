// SPDX-License-Identifier: GPL-3.0-or-later
//
// The charge-override section of a pack card. With an override active it says
// what it is and offers to end it; otherwise it is the form for starting one:
// a target, and how it ends (when full, at a clock time, or at a date and
// time), mirroring `thinkbatt charge`.
//
// The form's state lives here, not in the status payload, so the 3 s status
// refresh while the popup is open does not reset a half-filled form.

import QtQuick
import QtQuick.Layouts
import QtQuick.Controls as QQC2
import org.kde.kirigami as Kirigami
import org.kde.plasma.components as PlasmaComponents3
import "logic.js" as Logic

ColumnLayout {
    id: ctl

    required property var pack
    required property date now
    property bool busy: false
    signal action(var args)

    property string mode: Logic.MODE_FULL
    readonly property date deadline: mode === Logic.MODE_TIME
        ? Logic.nextOccurrence(hourBox.value, minuteBox.value, now)
        : Logic.combine(dayPicker.selected, hourBox.value, minuteBox.value)
    readonly property bool deadlineOk: mode === Logic.MODE_FULL || deadline.getTime() > now.getTime()

    spacing: Kirigami.Units.smallSpacing

    // Active override: describe it and offer the way back.
    RowLayout {
        visible: !!ctl.pack.override
        Layout.fillWidth: true
        Kirigami.Icon {
            source: "battery-full-charging"
            implicitWidth: Kirigami.Units.iconSizes.small
            implicitHeight: Kirigami.Units.iconSizes.small
        }
        PlasmaComponents3.Label {
            Layout.fillWidth: true
            wrapMode: Text.Wrap
            text: ctl.pack.override ? Logic.describeOverride(ctl.pack.override, ctl.now) : ""
        }
        PlasmaComponents3.Button {
            text: i18n("Back to band")
            icon.name: "edit-undo"
            enabled: !ctl.busy
            onClicked: ctl.action(Logic.resetArgs(ctl.pack.name))
            PlasmaComponents3.ToolTip.text: i18n("End the override and return to the %1-%2% resting band", ctl.pack.band_start, ctl.pack.band_stop)
            PlasmaComponents3.ToolTip.visible: hovered
        }
    }

    PlasmaComponents3.Label {
        visible: !ctl.pack.override && !ctl.pack.has_thresholds
        Layout.fillWidth: true
        wrapMode: Text.Wrap
        opacity: 0.7
        text: i18n("This pack has no charge thresholds, so it cannot be overridden.")
    }

    // The form for a new override.
    ColumnLayout {
        visible: !ctl.pack.override && ctl.pack.has_thresholds
        Layout.fillWidth: true
        spacing: Kirigami.Units.smallSpacing

        RowLayout {
            PlasmaComponents3.Label {
                text: i18n("Charge to")
            }
            PlasmaComponents3.SpinBox {
                id: targetBox
                from: 50
                to: 100
                stepSize: 5
                value: 100
                editable: true
                textFromValue: (v) => v >= 100 ? i18n("Full") : v + "%"
                valueFromText: (t) => /full/i.test(t) ? 100 : parseInt(t, 10)
            }
        }

        QQC2.ButtonGroup {
            id: modeGroup
        }
        PlasmaComponents3.RadioButton {
            text: i18n("Until full")
            checked: ctl.mode === Logic.MODE_FULL
            QQC2.ButtonGroup.group: modeGroup
            onToggled: if (checked) ctl.mode = Logic.MODE_FULL
        }
        PlasmaComponents3.RadioButton {
            text: i18n("Until a time")
            checked: ctl.mode === Logic.MODE_TIME
            QQC2.ButtonGroup.group: modeGroup
            onToggled: if (checked) ctl.mode = Logic.MODE_TIME
        }
        PlasmaComponents3.RadioButton {
            text: i18n("Until a date and time")
            checked: ctl.mode === Logic.MODE_DATETIME
            QQC2.ButtonGroup.group: modeGroup
            onToggled: if (checked) ctl.mode = Logic.MODE_DATETIME
        }

        DayPicker {
            id: dayPicker
            visible: ctl.mode === Logic.MODE_DATETIME
            Layout.fillWidth: true
            today: ctl.now
            selected: new Date(ctl.now.getFullYear(), ctl.now.getMonth(), ctl.now.getDate() + 1)
        }

        RowLayout {
            visible: ctl.mode !== Logic.MODE_FULL
            PlasmaComponents3.Label {
                text: i18n("At")
            }
            PlasmaComponents3.SpinBox {
                id: hourBox
                from: 0
                to: 23
                value: 9
                editable: true
                wrap: true
                textFromValue: (v) => Logic.pad2(v)
                valueFromText: (t) => parseInt(t, 10)
            }
            PlasmaComponents3.Label {
                text: ":"
            }
            PlasmaComponents3.SpinBox {
                id: minuteBox
                from: 0
                to: 59
                stepSize: 5
                value: 0
                editable: true
                wrap: true
                textFromValue: (v) => Logic.pad2(v)
                valueFromText: (t) => parseInt(t, 10)
            }
        }

        PlasmaComponents3.Label {
            visible: ctl.mode !== Logic.MODE_FULL
            Layout.fillWidth: true
            wrapMode: Text.Wrap
            opacity: ctl.deadlineOk ? 0.7 : 1
            color: ctl.deadlineOk ? Kirigami.Theme.textColor : Kirigami.Theme.negativeTextColor
            text: ctl.deadlineOk
                ? i18n("Ends %1, then back to %2-%3%", Logic.describeWhen(ctl.deadline, ctl.now), ctl.pack.band_start, ctl.pack.band_stop)
                : i18n("That time has already passed.")
        }

        PlasmaComponents3.Label {
            visible: ctl.pack.calibration_running
            Layout.fillWidth: true
            wrapMode: Text.Wrap
            color: Kirigami.Theme.neutralTextColor
            text: i18n("Starting an override cancels the running calibration.")
        }

        PlasmaComponents3.Button {
            text: targetBox.value >= 100 ? i18n("Charge to full") : i18n("Charge to %1%", targetBox.value)
            icon.name: "battery-full-charging"
            enabled: !ctl.busy && ctl.deadlineOk
            onClicked: ctl.action(Logic.chargeArgs(ctl.pack.name, targetBox.value, ctl.mode, ctl.deadline))
        }
    }
}
