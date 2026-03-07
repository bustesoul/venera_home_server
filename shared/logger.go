package shared

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

type LogLevel int

const (
	DebugLevel LogLevel = iota
	InfoLevel
	WarnLevel
	ErrorLevel
)

type LevelLogger struct {
	base  *log.Logger
	level LogLevel
}

func NewLevelLogger(base *log.Logger, level string) *LevelLogger {
	if base == nil {
		base = log.New(os.Stdout, "", log.LstdFlags)
	}
	return &LevelLogger{base: base, level: parseLogLevel(level)}
}

func parseLogLevel(level string) LogLevel {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return DebugLevel
	case "warn", "warning":
		return WarnLevel
	case "error":
		return ErrorLevel
	default:
		return InfoLevel
	}
}

func (l *LevelLogger) Base() *log.Logger {
	return l.base
}

func (l *LevelLogger) Writer() io.Writer {
	return l.base.Writer()
}

func (l *LevelLogger) EnabledDebug() bool {
	return l != nil && l.level <= DebugLevel
}

func (l *LevelLogger) Debugf(format string, args ...any) {
	if l != nil && l.level <= DebugLevel {
		l.base.Printf("DEBUG %s", fmt.Sprintf(format, args...))
	}
}

func (l *LevelLogger) Infof(format string, args ...any) {
	if l != nil && l.level <= InfoLevel {
		l.base.Printf("INFO %s", fmt.Sprintf(format, args...))
	}
}

func (l *LevelLogger) Errorf(format string, args ...any) {
	if l != nil && l.level <= ErrorLevel {
		l.base.Printf("ERROR %s", fmt.Sprintf(format, args...))
	}
}

func (l *LevelLogger) Fatalf(format string, args ...any) {
	if l == nil {
		log.Fatalf(format, args...)
	}
	l.base.Fatalf("FATAL %s", fmt.Sprintf(format, args...))
}
