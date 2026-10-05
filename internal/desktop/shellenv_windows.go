package desktop

// ImportShellEnv is a no-op on Windows: GUI apps inherit the user
// environment from Explorer.
func ImportShellEnv() {}
