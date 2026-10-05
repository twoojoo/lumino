// Package notify sends desktop notifications: the freedesktop
// org.freedesktop.Notifications D-Bus service on Linux/BSD (GNOME, KDE, …),
// Notification Center on macOS and toast notifications on Windows.
package notify

// Urgency levels per the freedesktop spec.
const (
	Low      byte = 0
	Normal   byte = 1
	Critical byte = 2
)
