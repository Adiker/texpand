package appfilter

import (
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// kwinScript pushes the active window class to texpand over DBus on every
// activation. It supports both Plasma 6 (windowActivated) and Plasma 5
// (clientActivated) scripting APIs and is read-only with respect to KWin.
const kwinScript = `function texpandNotify(w) {
    callDBus("io.github.texpand", "/io/github/texpand",
        "io.github.texpand.Autocorrect1", "SetActiveWindow",
        w ? String(w.resourceClass) : "");
}
if (typeof workspace.windowActivated !== "undefined") {
    workspace.windowActivated.connect(texpandNotify);
    if (workspace.activeWindow) { texpandNotify(workspace.activeWindow); }
} else {
    workspace.clientActivated.connect(texpandNotify);
    if (workspace.activeClient) { texpandNotify(workspace.activeClient); }
}
`

const kwinPluginName = "texpand-activewindow"

// LoadKWinScript registers the window-tracking script with KWin and starts
// it. It returns a cleanup function that unloads the script. Errors are
// expected on non-KDE compositors; the caller degrades to unknown-app
// behaviour.
func LoadKWinScript(conn *dbus.Conn) (func(), error) {
	f, err := os.CreateTemp("", "texpand-kwin-*.js")
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(kwinScript); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	f.Close()

	scripting := conn.Object("org.kde.KWin", "/Scripting")
	// A stale copy may survive a previous crash; unload first.
	scripting.Call("org.kde.kwin.Scripting.unloadScript", 0, kwinPluginName)

	var id int32
	err = scripting.Call("org.kde.kwin.Scripting.loadScript", 0, f.Name(), kwinPluginName).Store(&id)
	if err != nil {
		os.Remove(f.Name())
		return nil, fmt.Errorf("kwin loadScript: %w", err)
	}

	// Plasma 6 exposes the script at /Scripting/ScriptN, Plasma 5 at /N.
	started := false
	for _, p := range []dbus.ObjectPath{
		dbus.ObjectPath(fmt.Sprintf("/Scripting/Script%d", id)),
		dbus.ObjectPath(fmt.Sprintf("/%d", id)),
	} {
		if call := conn.Object("org.kde.KWin", p).Call("org.kde.kwin.Script.run", 0); call.Err == nil {
			started = true
			break
		}
	}
	if !started {
		scripting.Call("org.kde.kwin.Scripting.unloadScript", 0, kwinPluginName)
		os.Remove(f.Name())
		return nil, fmt.Errorf("kwin script loaded but could not be started")
	}

	cleanup := func() {
		scripting.Call("org.kde.kwin.Scripting.unloadScript", 0, kwinPluginName)
		os.Remove(f.Name())
	}
	return cleanup, nil
}
