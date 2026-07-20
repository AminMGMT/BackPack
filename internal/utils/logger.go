package utils

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
)

type CustomFormatter struct{}

func (f *CustomFormatter) Format(entry *logrus.Entry) ([]byte, error) {
	timestamp := entry.Time.Format("02-Jan 15:04:05")

	level := strings.ToUpper(entry.Level.String())
	coloredLevel := f.colorize(entry.Level, level)

	logMessage := fmt.Sprintf("%s [%s] %s\n", timestamp, coloredLevel, entry.Message)

	return []byte(logMessage), nil
}

// colorize the output
func (f *CustomFormatter) colorize(level logrus.Level, text string) string {
	switch level {
	case logrus.PanicLevel:
		return "\033[1;31m" + text + "\033[0m" // Bright Red for PANIC
	case logrus.FatalLevel:
		return "\033[1;31m" + text + "\033[0m" // Bright Red for FATAL
	case logrus.ErrorLevel:
		return "\033[31m" + text + "\033[0m" // Red for ERROR
	case logrus.WarnLevel:
		return "\033[33m" + text + "\033[0m" // Yellow for WARN
	case logrus.InfoLevel:
		return "\033[32m" + text + "\033[0m" // Green for INFO
	case logrus.DebugLevel:
		return "\033[34m" + text + "\033[0m" // Blue for DEBUG
	case logrus.TraceLevel:
		return "\033[36m" + text + "\033[0m" // Cyan for TRACE
	default:
		return text
	}
}

func NewLogger(logLevel string) *logrus.Logger {
	return NewLoggerWithFormat(logLevel, "")
}

// NewLoggerWithFormat builds a logger in either the human-readable format or
// JSON.
//
// JSON exists for anyone shipping logs somewhere that parses them — journald's
// structured fields, a log collector, a script. The default stays the coloured
// text format, because the usual way these logs are read is a person running
// journalctl on their own server, and JSON is worse for that.
func NewLoggerWithFormat(logLevel, format string) *logrus.Logger {
	log := logrus.New()

	// Select log level
	log.SetOutput(os.Stdout)

	// parsed in the config.go already. so err is nil
	parseLevel, _ := logrus.ParseLevel(logLevel)

	log.SetLevel(parseLevel)

	if strings.EqualFold(strings.TrimSpace(format), "json") {
		log.SetFormatter(&logrus.JSONFormatter{
			TimestampFormat: time.RFC3339,
		})
	} else {
		log.SetFormatter(&CustomFormatter{})
	}

	return log
}
