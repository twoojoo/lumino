package notify

import (
	"os/exec"
	"strings"
)

// Notifier sends notifications through Notification Center; safe for
// concurrent use.
type Notifier struct{ app string }

// New returns a notifier for app.
func New(app string) *Notifier { return &Notifier{app: app} }

// The script is constant: texts (cluster-controlled) are passed as argv.
const notifyScript = `on run argv
	display notification (item 2 of argv) with title (item 1 of argv)
end run`

const notifyScriptSound = `on run argv
	display notification (item 2 of argv) with title (item 1 of argv) sound name "Basso"
end run`

// Send shows a notification. Errors are returned, never fatal.
func (n *Notifier) Send(summary, body string, urgency byte) error {
	script := notifyScript
	if urgency == Critical {
		script = notifyScriptSound
	}
	clean := func(s string) string { return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "") }
	return exec.Command("osascript", "-e", script, clean(summary), clean(body)).Run()
}
