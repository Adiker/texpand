// Package control provides the runtime-control IPC for the daemon: a
// session-bus DBus object with enable/disable/toggle/status methods (also
// the sink for the KWin active-window script), a matching CLI client, and
// desktop notifications. Control commands talk to the already running
// daemon; they never start a second keyboard monitor.
package control

import (
	"errors"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	BusName   = "io.github.texpand"
	Interface = "io.github.texpand.Autocorrect1"
)

// ObjectPath is the exported DBus object path.
const ObjectPath = dbus.ObjectPath("/io/github/texpand")

// Status is a snapshot of the autocorrect subsystem.
type Status struct {
	Enabled    bool
	DictReady  bool
	Words      uint32
	Candidates uint32
	ActiveApp  string
	DictState  string
	DictError  string
}

// Daemon is what the DBus server needs from the running daemon. All
// methods must be safe to call from the DBus goroutine.
type Daemon interface {
	SetAutocorrectEnabled(bool)
	AutocorrectStatus() Status
	SetActiveWindow(class string)
}

// handler adapts Daemon to the DBus method table.
type handler struct {
	d Daemon
}

func (h *handler) Enable() *dbus.Error {
	h.d.SetAutocorrectEnabled(true)
	return nil
}

func (h *handler) Disable() *dbus.Error {
	h.d.SetAutocorrectEnabled(false)
	return nil
}

func (h *handler) Toggle() (bool, *dbus.Error) {
	st := h.d.AutocorrectStatus()
	h.d.SetAutocorrectEnabled(!st.Enabled)
	return !st.Enabled, nil
}

func (h *handler) Status() (bool, bool, uint32, uint32, string, *dbus.Error) {
	st := h.d.AutocorrectStatus()
	return st.Enabled, st.DictReady, st.Words, st.Candidates, st.ActiveApp, nil
}

// StatusDetails extends Status without changing its existing D-Bus signature,
// so older clients remain compatible.
func (h *handler) StatusDetails() (bool, string, uint32, uint32, string, string, *dbus.Error) {
	st := h.d.AutocorrectStatus()
	return st.Enabled, st.DictState, st.Words, st.Candidates, st.ActiveApp, st.DictError, nil
}

func (h *handler) SetActiveWindow(class string) *dbus.Error {
	h.d.SetActiveWindow(class)
	return nil
}

// Server owns the daemon's session-bus connection.
type Server struct {
	Conn *dbus.Conn
}

// StartServer connects to the session bus, claims the texpand name and
// exports the control object.
func StartServer(d Daemon) (*Server, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("session bus: %w", err)
	}
	reply, err := conn.RequestName(BusName, dbus.NameFlagDoNotQueue)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("request name: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		return nil, fmt.Errorf("another texpand instance owns %s", BusName)
	}
	if err := conn.Export(&handler{d: d}, ObjectPath, Interface); err != nil {
		conn.Close()
		return nil, fmt.Errorf("export: %w", err)
	}
	return &Server{Conn: conn}, nil
}

// Close releases the bus name and connection.
func (s *Server) Close() {
	if s.Conn != nil {
		s.Conn.ReleaseName(BusName)
		s.Conn.Close()
	}
}

// Notify sends a desktop notification (best effort).
func (s *Server) Notify(summary, body string) {
	if s == nil || s.Conn == nil {
		return
	}
	obj := s.Conn.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications")
	obj.Call("org.freedesktop.Notifications.Notify", 0,
		"texpand", uint32(0), "input-keyboard", summary, body,
		[]string{}, map[string]dbus.Variant{}, int32(2000))
}

// ClientCommand executes an autocorrect CLI subcommand against the running
// daemon and returns printable output.
func ClientCommand(cmd string) (string, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return "", fmt.Errorf("session bus: %w", err)
	}
	defer conn.Close()
	obj := conn.Object(BusName, ObjectPath)

	fail := func(err error) (string, error) {
		return "", fmt.Errorf("is the texpand service running? (%v)", err)
	}

	switch cmd {
	case "enable":
		if call := obj.Call(Interface+".Enable", 0); call.Err != nil {
			return fail(call.Err)
		}
		return "autocorrect enabled", nil
	case "disable":
		if call := obj.Call(Interface+".Disable", 0); call.Err != nil {
			return fail(call.Err)
		}
		return "autocorrect disabled", nil
	case "toggle":
		var enabled bool
		if err := obj.Call(Interface+".Toggle", 0).Store(&enabled); err != nil {
			return fail(err)
		}
		if enabled {
			return "autocorrect enabled", nil
		}
		return "autocorrect disabled", nil
	case "status":
		var st Status
		err := obj.Call(Interface+".StatusDetails", 0).Store(
			&st.Enabled, &st.DictState, &st.Words, &st.Candidates, &st.ActiveApp, &st.DictError,
		)
		if err != nil {
			var dbusErr dbus.Error
			if !errors.As(err, &dbusErr) || dbusErr.Name != "org.freedesktop.DBus.Error.UnknownMethod" {
				return fail(err)
			}
			// Compatibility with a daemon from before StatusDetails.
			if err := obj.Call(Interface+".Status", 0).Store(
				&st.Enabled, &st.DictReady, &st.Words, &st.Candidates, &st.ActiveApp,
			); err != nil {
				return fail(err)
			}
			st.DictState = "loading"
			if st.DictReady {
				st.DictState = "ready"
			}
		}
		return formatStatus(st), nil
	default:
		return "", fmt.Errorf("unknown autocorrect command %q (use enable|disable|toggle|status)", cmd)
	}
}

func formatStatus(st Status) string {
	state := "disabled"
	if st.Enabled {
		state = "enabled"
	}
	dictionary := st.DictState
	if dictionary == "" {
		dictionary = "idle"
	}
	if dictionary == "ready" {
		dictionary = fmt.Sprintf("ready (%d word forms, %d candidate pairs)", st.Words, st.Candidates)
	} else if dictionary == "failed" && st.DictError != "" {
		dictionary = fmt.Sprintf("failed (%s)", st.DictError)
	}
	if st.ActiveApp == "" {
		st.ActiveApp = "(unknown)"
	}
	return fmt.Sprintf("autocorrect: %s\ndictionary:  %s\nactive app:  %s", state, dictionary, st.ActiveApp)
}
