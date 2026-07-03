//go:build !darwin

package clip

// consoleUID is only meaningful on macOS (see console_darwin.go).
func consoleUID() (int, bool) { return 0, false }
