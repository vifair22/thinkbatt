// SPDX-License-Identifier: GPL-3.0-or-later
//
// A horizontal state-of-charge bar with the band the pack is held at drawn as
// an outlined region over it, so "57%, band 75-80" reads at a glance. During a
// charge override the region is the override's target and takes the neutral
// (attention) colour.

import QtQuick
import org.kde.kirigami as Kirigami

Item {
    id: bar

    property int capacity: 0
    property int bandStart: 0
    property int bandStop: 0
    property bool showBand: true
    property bool override: false

    implicitHeight: Kirigami.Units.gridUnit * 0.8

    Rectangle {
        id: track
        anchors.fill: parent
        radius: Kirigami.Units.cornerRadius
        color: Qt.alpha(Kirigami.Theme.textColor, 0.08)
        border.color: Qt.alpha(Kirigami.Theme.textColor, 0.25)
        border.width: 1
        clip: true

        Rectangle {
            width: track.width * Math.max(0, Math.min(100, bar.capacity)) / 100
            height: track.height
            radius: track.radius
            color: Kirigami.Theme.highlightColor
        }

        Rectangle {
            visible: bar.showBand && bar.bandStop > bar.bandStart
            x: track.width * bar.bandStart / 100
            width: Math.max(2, track.width * (bar.bandStop - bar.bandStart) / 100)
            height: track.height
            color: Qt.alpha(bar.override ? Kirigami.Theme.neutralTextColor : Kirigami.Theme.positiveTextColor, 0.25)
            border.color: bar.override ? Kirigami.Theme.neutralTextColor : Kirigami.Theme.positiveTextColor
            border.width: 2
        }
    }
}
