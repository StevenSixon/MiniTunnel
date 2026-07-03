package clip

import (
	"os"
	"syscall"
)

// consoleUID returns the uid of the user logged in at the console, read from
// the owner of /dev/console — the standard way for a macOS daemon to find the
// GUI session it should act in.
func consoleUID() (int, bool) {
	fi, err := os.Stat("/dev/console")
	if err != nil {
		return 0, false
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
