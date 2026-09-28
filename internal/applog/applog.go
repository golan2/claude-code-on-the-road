// Package applog configures the standard library's log package to write to
// both stdout (so the console behavior GoApp already had is unchanged) and a
// persistent log file, so a run's history survives after the terminal window
// is gone or the process has crashed.
package applog

import (
	"fmt"
	"io"
	"log"
	"os"
)

// Init opens (or creates) the log file at path in append mode — so restarts
// accumulate history instead of overwriting it — and points the standard
// logger at both that file and stdout, with a date+time prefix on every
// line. The caller is responsible for closing the returned file at shutdown.
func Init(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.SetFlags(log.Ldate | log.Ltime)
	return f, nil
}
