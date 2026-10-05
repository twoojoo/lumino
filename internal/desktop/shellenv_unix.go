//go:build !windows

package desktop

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"
)

const envMarker = "__LUMINO_ENV__"

// ImportShellEnv copies the login shell environment into the process when
// lumino was started from a desktop launcher (Finder, Dock, app menu): those
// get a minimal environment, so kubeconfig exec plugins (aws,
// gke-gcloud-auth-plugin, kubelogin, …) installed in shell-configured paths
// wouldn't be found. PATH is replaced; other variables are only added.
// Started from a terminal, the environment is already right and nothing
// happens.
func ImportShellEnv() {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		return
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// -i -l: load both login and interactive rc files (PATH is often set in
	// .zshrc/.bashrc); markers fence off anything the rc files print.
	cmd := exec.CommandContext(ctx, shell, "-ilc", "printf '"+envMarker+"'; command env -0; printf '"+envMarker+"'")
	cmd.Stdin = nil
	cmd.Stderr = nil
	// rc files may start background jobs that inherit stdout: don't wait for
	// their EOF after the shell is gone, or the app never starts
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return
	}
	for k, v := range parseEnvBlock(out) {
		if k == "PATH" || os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}
}

// parseEnvBlock extracts NUL-separated KEY=VALUE pairs between markers.
func parseEnvBlock(out []byte) map[string]string {
	i := bytes.Index(out, []byte(envMarker))
	j := bytes.LastIndex(out, []byte(envMarker))
	if i < 0 || j <= i {
		return nil
	}
	env := map[string]string{}
	for _, kv := range bytes.Split(out[i+len(envMarker):j], []byte{0}) {
		k, v, ok := strings.Cut(string(kv), "=")
		if !ok || k == "" || skipEnv[k] {
			continue
		}
		env[k] = v
	}
	return env
}

// skipEnv lists shell-session variables that must not leak into lumino.
var skipEnv = map[string]bool{"_": true, "PWD": true, "OLDPWD": true, "SHLVL": true, "PS1": true}
