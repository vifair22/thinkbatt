// SPDX-License-Identifier: GPL-3.0-or-later
//
// One battery pack in the popup: charge and band at a glance, the live
// readings, the override section, and calibration. Actions are emitted as CLI
// argument lists; main.qml runs them.

import QtQuick
import QtQuick.Layouts
import org.kde.kirigami as Kirigami
import org.kde.plasma.components as PlasmaComponents3
import "logic.js" as Logic

ColumnLayout {
    id: card

    required property var pack
    required property date now
    property bool busy: false
    signal action(var args)

    property bool confirmCalibrate: false
    readonly property var band: Logic.effectiveBand(pack)
    readonly property real health: Logic.healthPercent(pack.energy_full_uwh, pack.energy_full_design_uwh)

    spacing: Kirigami.Units.smallSpacing

    RowLayout {
        Layout.fillWidth: true
        Kirigami.Icon {
            source: Logic.iconName(card.pack.capacity, Logic.isCharging(card.pack))
            implicitWidth: Kirigami.Units.iconSizes.medium
            implicitHeight: Kirigami.Units.iconSizes.medium
        }
        Kirigami.Heading {
            level: 3
            text: card.pack.name
        }
        Item {
            Layout.fillWidth: true
        }
        Kirigami.Heading {
            level: 3
            text: card.pack.capacity + "%"
        }
        PlasmaComponents3.Label {
            opacity: 0.7
            text: card.pack.status
        }
    }

    ChargeBar {
        Layout.fillWidth: true
        capacity: card.pack.capacity
        bandStart: card.band.start
        bandStop: card.band.stop
        showBand: card.pack.has_thresholds
        override: !!card.pack.override
    }

    GridLayout {
        Layout.fillWidth: true
        columns: 2
        columnSpacing: Kirigami.Units.largeSpacing
        rowSpacing: 0

        PlasmaComponents3.Label { text: i18n("Power"); opacity: 0.7 }
        PlasmaComponents3.Label { text: Logic.formatPower(card.pack.power_uw) }

        PlasmaComponents3.Label { text: i18n("Voltage"); opacity: 0.7 }
        PlasmaComponents3.Label { text: Logic.formatVoltage(card.pack.voltage_uv) }

        PlasmaComponents3.Label { text: i18n("Health"); opacity: 0.7; visible: card.health >= 0 }
        PlasmaComponents3.Label {
            visible: card.health >= 0
            text: i18n("%1% (%2 of %3 design)", card.health,
                       Logic.formatWh(card.pack.energy_full_uwh), Logic.formatWh(card.pack.energy_full_design_uwh))
        }

        PlasmaComponents3.Label { text: i18n("Band"); opacity: 0.7; visible: card.pack.has_thresholds }
        PlasmaComponents3.Label {
            visible: card.pack.has_thresholds
            text: card.pack.override
                ? i18n("%1-%2% (resting %3-%4%)", card.band.start, card.band.stop, card.pack.band_start, card.pack.band_stop)
                : i18n("%1-%2%", card.pack.band_start, card.pack.band_stop)
        }

        PlasmaComponents3.Label { text: i18n("Calibration"); opacity: 0.7 }
        PlasmaComponents3.Label {
            Layout.fillWidth: true
            wrapMode: Text.Wrap
            text: card.pack.calibration_running
                ? i18n("running: %1", card.pack.calibration_phase || i18n("starting"))
                : card.pack.last_calibration
                    ? i18n("last %1", Logic.describeLastCalibration(card.pack.last_calibration))
                    : i18n("never run")
        }
    }

    Kirigami.Separator {
        Layout.fillWidth: true
    }

    OverrideControls {
        Layout.fillWidth: true
        pack: card.pack
        now: card.now
        busy: card.busy
        onAction: (args) => card.action(args)
    }

    // Calibration. Starting one asks first: it drains the pack to the floor
    // and takes hours on AC, not something to trigger by a stray click.
    ColumnLayout {
        visible: Logic.canCalibrate(card.pack)
        Layout.fillWidth: true

        Kirigami.Separator {
            Layout.fillWidth: true
        }

        PlasmaComponents3.Button {
            visible: card.pack.calibration_running
            text: i18n("Cancel calibration")
            icon.name: "process-stop"
            enabled: !card.busy
            onClicked: card.action(Logic.cancelArgs())
        }

        PlasmaComponents3.Button {
            visible: !card.pack.calibration_running && !card.confirmCalibrate
            text: i18n("Calibrate now...")
            icon.name: "view-refresh"
            enabled: !card.busy && !card.pack.override
            onClicked: card.confirmCalibrate = true
            PlasmaComponents3.ToolTip.text: card.pack.override
                ? i18n("End the charge override first")
                : i18n("Run a fuel-gauge learning cycle on this pack")
            PlasmaComponents3.ToolTip.visible: hovered
        }

        ColumnLayout {
            visible: card.confirmCalibrate && !card.pack.calibration_running
            Layout.fillWidth: true
            PlasmaComponents3.Label {
                Layout.fillWidth: true
                wrapMode: Text.Wrap
                text: i18n("A learning cycle charges %1 to full, discharges it to the floor and recharges it. It needs AC throughout and takes several hours.", card.pack.name)
            }
            RowLayout {
                PlasmaComponents3.Button {
                    text: i18n("Start calibration")
                    icon.name: "view-refresh"
                    enabled: !card.busy
                    onClicked: {
                        card.confirmCalibrate = false;
                        card.action(Logic.calibrateArgs(card.pack.name));
                    }
                }
                PlasmaComponents3.Button {
                    text: i18n("Never mind")
                    onClicked: card.confirmCalibrate = false
                }
            }
        }
    }
}
