package desktop

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/godbus/dbus/v5"
)

// CursorSettings returns the desktop cursor theme and size (best effort):
// the freedesktop settings portal first (GNOME, KDE via its portal…), then
// gsettings. Empty/zero values mean unknown.
func CursorSettings() (theme string, size int) {
	theme, size = portalCursor()
	if theme == "" || size == 0 {
		t, s := gsettingsCursor()
		if theme == "" {
			theme = t
		}
		if size == 0 {
			size = s
		}
	}
	return theme, size
}

// ApplyCursorEnv exports XCURSOR_THEME/XCURSOR_SIZE when unset, so Gio's
// Wayland backend loads the same cursor as the rest of the desktop
// (it otherwise defaults to size 32).
func ApplyCursorEnv() {
	if os.Getenv("XCURSOR_SIZE") != "" && os.Getenv("XCURSOR_THEME") != "" {
		return
	}
	theme, size := CursorSettings()
	if os.Getenv("XCURSOR_THEME") == "" && theme != "" {
		_ = os.Setenv("XCURSOR_THEME", theme)
	}
	if os.Getenv("XCURSOR_SIZE") == "" && size > 0 {
		_ = os.Setenv("XCURSOR_SIZE", strconv.Itoa(size))
	}
}

func portalCursor() (string, int) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return "", 0
	}
	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	read := func(key string) any {
		var v dbus.Variant
		call := obj.Call("org.freedesktop.portal.Settings.Read", dbus.FlagNoAutoStart, "org.gnome.desktop.interface", key)
		if call.Err != nil || call.Store(&v) != nil {
			return nil
		}
		val := v.Value()
		// Read wraps the value in a second variant
		if inner, ok := val.(dbus.Variant); ok {
			val = inner.Value()
		}
		return val
	}
	var theme string
	var size int
	if s, ok := read("cursor-theme").(string); ok {
		theme = s
	}
	switch n := read("cursor-size").(type) {
	case int32:
		size = int(n)
	case uint32:
		size = int(n)
	case int64:
		size = int(n)
	}
	return theme, size
}

func gsettingsCursor() (string, int) {
	get := func(key string) string {
		ctx := exec.Command("gsettings", "get", "org.gnome.desktop.interface", key)
		done := make(chan struct{})
		var out []byte
		go func() { out, _ = ctx.Output(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			if ctx.Process != nil {
				_ = ctx.Process.Kill()
			}
			return ""
		}
		return strings.Trim(strings.TrimSpace(string(out)), "'")
	}
	theme := get("cursor-theme")
	size, _ := strconv.Atoi(strings.TrimPrefix(get("cursor-size"), "uint32 "))
	return theme, size
}
