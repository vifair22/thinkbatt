// SPDX-License-Identifier: GPL-3.0-or-later
//
// One powerdevil inhibition the plasmoid holds on the user's behalf: what it
// blocks (PolicyAgent RequiredPolicy bits) and the reason it is filed under.
// main.qml keeps one for "sleep and screen locking" and one for "sleep only".
//
// The cookie lives on plasmashell's D-Bus connection, so it and this object
// end together. set() tracks what was last asked for as well as the cookie:
// switching off while an AddInhibition is still in flight must not leave a
// block behind when its reply lands.

import QtQuick
import org.kde.plasma.workspace.dbus as DBus
import "logic.js" as Logic

QtObject {
    id: inhibition

    required property int types
    required property string reason

    property var cookie: null
    readonly property bool active: cookie !== null
    property bool wanted: false
    property bool pending: false

    // A call failed; main.qml shows the message.
    signal failed(string message)
    // The block was asked for or dropped; powerdevil's list changes shortly.
    signal changed()

    function set(on) {
        wanted = on;
        if (on && cookie === null && !pending) {
            pending = true;
            call("AddInhibition", [new DBus.uint32(types), new DBus.string(Logic.INHIBIT_APP), new DBus.string(reason)],
                 "(uss)",
                 (c) => {
                     pending = false;
                     if (wanted) {
                         cookie = c;
                     } else {
                         release(c);
                     }
                 },
                 () => { pending = false; });
        } else if (!on && cookie !== null) {
            const c = cookie;
            cookie = null;
            release(c);
        }
        changed();
    }

    // toggle flips what was last asked for, so a second middle-click while
    // the first AddInhibition is in flight cancels it rather than repeating it.
    function toggle() {
        set(!(active || (pending && wanted)));
    }

    function release(c) {
        call("ReleaseInhibition", [new DBus.uint32(c)], "(u)", null, null);
    }

    function call(member, args, signature, onReply, onError) {
        const reply = DBus.SessionBus.asyncCall(Logic.policyCall(member, args, signature));
        reply.finished.connect(() => {
            if (reply.isError) {
                inhibition.failed(reply.error.message());
                if (onError) {
                    onError();
                }
            } else if (onReply) {
                onReply(Logic.unwrap(reply.value));
            }
        });
    }
}
