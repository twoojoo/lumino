package notify

import (
	"os"
	"os/exec"
	"strings"
	"syscall"
)

// Notifier sends Windows toast notifications through PowerShell and the
// WinRT notification API (Windows 10+); safe for concurrent use.
type Notifier struct{ app string }

// New returns a notifier for app.
func New(app string) *Notifier { return &Notifier{app: app} }

// powershellAUMID is the AppUserModelID of Windows PowerShell, registered on
// every Windows install: toasts from unregistered AUMIDs are dropped.
const powershellAUMID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// toastScript is constant: the toast XML (built from cluster-controlled
// text) arrives through the environment, never as script source.
const toastScript = `[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications, ContentType = WindowsRuntime] > $null;` +
	`[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument, ContentType = WindowsRuntime] > $null;` +
	`$x = New-Object Windows.Data.Xml.Dom.XmlDocument; $x.LoadXml($env:LUMINO_TOAST_XML);` +
	`[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('` + powershellAUMID +
	`').Show([Windows.UI.Notifications.ToastNotification]::new($x))`

// xmlText escapes s for XML text and drops characters XML 1.0 forbids.
func xmlText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r != 0xFFFE && r != 0xFFFF {
			return r
		}
		return -1
	}, s)
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;").Replace(s)
}

// Send shows a notification. Errors are returned, never fatal.
func (n *Notifier) Send(summary, body string, urgency byte) error {
	scenario := ""
	if urgency == Critical {
		scenario = ` scenario="urgent"`
	}
	xml := `<toast` + scenario + `><visual><binding template="ToastGeneric"><text>` + xmlText(summary) +
		`</text><text>` + xmlText(body) + `</text></binding></visual></toast>`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toastScript)
	cmd.Env = append(os.Environ(), "LUMINO_TOAST_XML="+xml)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000} // CREATE_NO_WINDOW
	return cmd.Run()
}
