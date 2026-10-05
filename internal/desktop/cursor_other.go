//go:build !linux

package desktop

// ApplyCursorEnv is only needed on Linux (Wayland cursor theme/size).
func ApplyCursorEnv() {}
