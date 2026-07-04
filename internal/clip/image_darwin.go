package clip

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Image clipboard support, macOS only. Reading and writing go through
// osascript (bundled with macOS — no new dependency), which can coerce the
// pasteboard's image flavors (screenshot PNG, browser TIFF, …) to PNG bytes.
// All commands run via userCmd so the agent LaunchDaemon (root) reaches the
// console user's pasteboard.

// ImageSig reports whether the clipboard currently holds an image, and if so
// returns a cheap change signature (the `clipboard info` type+size listing).
// Polling compares signatures and only pulls the actual PNG — potentially
// megabytes through an osascript hex dump — when the signature changes.
func ImageSig() (string, bool) {
	out, err := userCmd("osascript", "-e", "clipboard info").Output()
	if err != nil {
		return "", false
	}
	s := string(out)
	if !strings.Contains(s, "PNGf") && !strings.Contains(s, "TIFF") {
		return "", false
	}
	return s, true
}

// ReadImage returns the clipboard image as PNG bytes.
func ReadImage() ([]byte, error) {
	// Output shape: «data PNGf89504E47...» — hex between the class tag and the
	// closing guillemet.
	out, err := userCmd("osascript", "-e", "the clipboard as «class PNGf»").Output()
	if err != nil {
		return nil, fmt.Errorf("osascript read image: %w", err)
	}
	s := strings.TrimSpace(string(out))
	i := strings.Index(s, "PNGf")
	j := strings.LastIndex(s, "»")
	if i < 0 || j < i+4 {
		return nil, fmt.Errorf("unexpected osascript clipboard output (%d bytes)", len(s))
	}
	return hex.DecodeString(s[i+4 : j])
}

// WriteImage replaces the clipboard contents with the given PNG bytes. The
// data goes through a briefly-lived temp file because osascript has no stdin
// path for binary; 0644 so the console user's osascript can read a root-owned
// file when the agent runs as a daemon.
func WriteImage(png []byte) error {
	f, err := os.CreateTemp("/tmp", "mtclip-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(png); err != nil {
		f.Close()
		return err
	}
	f.Close()
	if err := os.Chmod(f.Name(), 0o644); err != nil {
		return err
	}
	script := fmt.Sprintf("set the clipboard to (read (POSIX file %q) as «class PNGf»)", f.Name())
	if out, err := userCmd("osascript", "-e", script).CombinedOutput(); err != nil {
		return fmt.Errorf("osascript write image: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
