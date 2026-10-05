//go:build smoke

package notify

import "testing"

// Sends one real desktop notification (manual check).
func TestSmoke(t *testing.T) {
	if err := New("lumino").Send("lumino · test notification", "Desktop notifications work.", Normal); err != nil {
		t.Fatal(err)
	}
}
