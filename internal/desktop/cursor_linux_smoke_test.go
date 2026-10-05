//go:build smoke && linux

package desktop

import "testing"

func TestCursorSettings(t *testing.T) {
	theme, size := CursorSettings()
	t.Logf("theme=%q size=%d", theme, size)
	if size == 0 {
		t.Fatal("no cursor size")
	}
}
