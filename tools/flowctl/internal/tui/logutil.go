package tui

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// LogWriter is a simple file-append logger written at startup.
// If the log path is empty, all writes are discarded.
type LogWriter struct {
	mu   sync.Mutex
	file *os.File
}

// NewLogWriter opens (or creates) the log file at path for appending.
// If path is empty, returns a no-op writer.
func NewLogWriter(path string) *LogWriter {
	if path == "" {
		return &LogWriter{file: nil}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0644)
	if err != nil {
		// Silently degrade — log is best-effort
		return &LogWriter{file: nil}
	}
	return &LogWriter{file: f}
}

// Close closes the underlying file, if any.
func (l *LogWriter) Close() {
	if l.file != nil {
		l.file.Close()
	}
}

// Log writes a structured log entry: TIMESTAMP [LEVEL] component: message
func (l *LogWriter) Log(level, component, message string) {
	if l.file == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.file, "%s [%s] %s: %s\n", time.Now().UTC().Format(time.RFC3339), level, component, message)
}
