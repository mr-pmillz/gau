package progress

import (
	"io"
)

// NewDisplayForTest exposes the internal mode-explicit constructor so
// tests can drive the non-TTY code path without relying on os.Stderr's
// runtime mode. Production callers must use NewDisplay.
func NewDisplayForTest(w io.Writer, t *Tracker, isTTY bool) *Display {
	return newDisplayWithMode(w, t, isTTY)
}
