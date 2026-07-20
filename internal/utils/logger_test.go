package utils

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONLoggerEmitsParsableLines(t *testing.T) {
	log := NewLoggerWithFormat("info", "json")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.WithField("tunnel", "client-4242").Info("control channel established")

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &entry); err != nil {
		t.Fatalf("JSON logging produced a line that will not parse: %v\n%s", err, buf.String())
	}
	if entry["msg"] != "control channel established" {
		t.Fatalf("message field = %v", entry["msg"])
	}
	if entry["tunnel"] != "client-4242" {
		t.Fatalf("structured field lost: %v", entry)
	}
	if entry["time"] == nil || entry["level"] == nil {
		t.Fatalf("a log line must carry a timestamp and a level: %v", entry)
	}
}

func TestDefaultLoggerStaysHumanReadable(t *testing.T) {
	// The default must not silently become JSON — journalctl is how these logs
	// are normally read.
	log := NewLoggerWithFormat("info", "")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.Info("hello")

	if strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Fatalf("default format became JSON: %s", buf.String())
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("message missing from text output: %s", buf.String())
	}
}

func TestJSONFormatIsCaseInsensitive(t *testing.T) {
	log := NewLoggerWithFormat("info", " JSON ")
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.Info("x")
	if !strings.HasPrefix(strings.TrimSpace(buf.String()), "{") {
		t.Fatalf(`" JSON " should select JSON, got: %s`, buf.String())
	}
}
