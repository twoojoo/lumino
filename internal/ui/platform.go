package ui

import "runtime"

// AppName is the product name shown to users (binary, packages and
// directories stay lowercase "lumino").
const AppName = "Lumino"

// nativeWindowControls is true where the OS draws the window buttons over
// our titlebar (macOS traffic lights, see third_party/_gio-patches).
var nativeWindowControls = runtime.GOOS == "darwin"

// trafficLightsWidth is the titlebar space left free for the macOS buttons.
const trafficLightsWidth = 78

// shortcutLabel names a key.ModShortcut combination as the platform does.
func shortcutLabel(k string) string {
	if runtime.GOOS == "darwin" {
		return "Cmd " + k
	}
	return "Ctrl " + k
}
