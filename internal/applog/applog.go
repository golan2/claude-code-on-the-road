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
	"time"
)

// timestampFormat is the layout prefixed to every log line. The standard
// library's log.Ldate|log.Ltime flags render as "2006/01/02 15:04:05" with
// no way to configure the separator, so timestampWriter below takes over
// prefixing entirely (with log.SetFlags(0)) to get dashes instead.
const timestampFormat = "2006-01-02 15:04:05"

// timestampWriter prepends a timestampFormat-formatted timestamp to every
// line written through it, then forwards the result to w in a single Write.
type timestampWriter struct {
	w io.Writer
}

func (t timestampWriter) Write(p []byte) (int, error) {
	line := append([]byte(time.Now().Format(timestampFormat)+" "), p...)
	if _, err := t.w.Write(line); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Init opens (or creates) the log file at path in append mode — so restarts
// accumulate history instead of overwriting it — and points the standard
// logger at both that file and stdout, with a timestampFormat-formatted
// date+time prefix on every line. The caller is responsible for closing the
// returned file at shutdown.
func Init(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file %q: %w", path, err)
	}
	log.SetOutput(timestampWriter{w: io.MultiWriter(os.Stdout, f)})
	log.SetFlags(0)
	return f, nil
}
