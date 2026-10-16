// SPDX-License-Identifier: GPL-3.0-or-later
//
// A compact month calendar for picking an override's end date. Built on Qt's
// own MonthGrid rather than Kirigami Addons' DatePopup, which sizes itself from
// a Kirigami application window that a plasmoid popup does not have. Days
// before today are shown but not selectable.
//
// Each cell's date is rebuilt from its year/month/day roles in local time:
// MonthGrid's own date role has been UTC midnight in some Qt releases, which
// lands on the previous day west of Greenwich.

pragma ComponentBehavior: Bound

import QtQuick
import QtQuick.Layouts
import QtQuick.Controls as QQC2
import org.kde.kirigami as Kirigami
import org.kde.plasma.components as PlasmaComponents3

ColumnLayout {
    id: picker

    property date selected: new Date()
    property date today: new Date()

    property int month: selected.getMonth()
    property int year: selected.getFullYear()

    spacing: 0

    function stepMonth(delta) {
        const d = new Date(year, month + delta, 1);
        month = d.getMonth();
        year = d.getFullYear();
    }

    function dayStart(d) {
        return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
    }

    RowLayout {
        Layout.fillWidth: true
        PlasmaComponents3.ToolButton {
            icon.name: "go-previous"
            enabled: new Date(picker.year, picker.month, 1).getTime() > picker.dayStart(picker.today)
            onClicked: picker.stepMonth(-1)
            PlasmaComponents3.ToolTip.text: i18n("Previous month")
            PlasmaComponents3.ToolTip.visible: hovered
        }
        PlasmaComponents3.Label {
            Layout.fillWidth: true
            horizontalAlignment: Text.AlignHCenter
            text: Qt.locale().standaloneMonthName(picker.month, Locale.LongFormat) + " " + picker.year
        }
        PlasmaComponents3.ToolButton {
            icon.name: "go-next"
            onClicked: picker.stepMonth(1)
            PlasmaComponents3.ToolTip.text: i18n("Next month")
            PlasmaComponents3.ToolTip.visible: hovered
        }
    }

    QQC2.DayOfWeekRow {
        Layout.fillWidth: true
        locale: grid.locale
        delegate: PlasmaComponents3.Label {
            required property string shortName
            horizontalAlignment: Text.AlignHCenter
            opacity: 0.6
            text: shortName
            font: Kirigami.Theme.smallFont
        }
    }

    QQC2.MonthGrid {
        id: grid
        Layout.fillWidth: true
        month: picker.month
        year: picker.year
        locale: Qt.locale()

        delegate: Item {
            id: cell
            required property var model
            readonly property date day: new Date(model.year, model.month, model.day)
            readonly property bool inMonth: model.month === grid.month
            readonly property bool past: picker.dayStart(day) < picker.dayStart(picker.today)
            readonly property bool chosen: picker.dayStart(day) === picker.dayStart(picker.selected)

            implicitWidth: Kirigami.Units.gridUnit * 1.6
            implicitHeight: Kirigami.Units.gridUnit * 1.4

            Rectangle {
                anchors.fill: parent
                anchors.margins: 1
                radius: Kirigami.Units.cornerRadius
                visible: cell.chosen || tap.hovered
                color: cell.chosen ? Kirigami.Theme.highlightColor : Qt.alpha(Kirigami.Theme.highlightColor, 0.2)
            }
            PlasmaComponents3.Label {
                anchors.centerIn: parent
                text: cell.model.day
                font.bold: cell.model.today
                color: cell.chosen ? Kirigami.Theme.highlightedTextColor : Kirigami.Theme.textColor
                opacity: !cell.inMonth ? 0.25 : cell.past ? 0.35 : 1
            }
            HoverHandler {
                id: tap
                enabled: !cell.past
            }
            TapHandler {
                enabled: !cell.past
                onTapped: picker.selected = cell.day
            }
        }
    }
}
