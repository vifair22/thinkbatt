// SPDX-License-Identifier: GPL-3.0-or-later

import QtQuick
import QtQuick.Layouts
import QtQuick.Controls as QQC2
import org.kde.kirigami as Kirigami
import org.kde.kcmutils as KCM

KCM.SimpleKCM {
    property alias cfg_command: commandField.text

    Kirigami.FormLayout {
        QQC2.TextField {
            id: commandField
            Kirigami.FormData.label: i18n("thinkbatt command:")
            placeholderText: "thinkbatt"
        }
        QQC2.Label {
            Layout.maximumWidth: Kirigami.Units.gridUnit * 24
            wrapMode: Text.Wrap
            font: Kirigami.Theme.smallFont
            text: i18n("Run through a shell, so a path or an environment prefix works, e.g. to reach a development daemon: env THINKBATT_SOCKET=/run/thinkbattd-test/control.sock ~/git/home/thinkbatt/build/bin/thinkbatt")
        }
    }
}
