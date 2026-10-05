package desktop

import (
	"os/exec"
	"runtime"
)

// openCommand returns the command that opens target with the default handler.
func openCommand(target string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		return exec.Command("xdg-open", target)
	}
}

// Open opens a URL or file with the desktop's default handler.
func Open(target string) error {
	cmd := openCommand(target)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap
	return nil
}
