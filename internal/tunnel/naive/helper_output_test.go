package naive

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

func init() {
	if os.Getenv("BACKPACK_HELPER_OUTPUT_FIXTURE") != "1" {
		return
	}
	checking := len(os.Args) > 2 && os.Args[2] == "-test"
	phase := "runtime"
	if checking {
		phase = "preflight"
	}
	fmtOutput := func(file *os.File, stream string) { _, _ = io.WriteString(file, phase+" "+stream+" diagnostic\n") }
	fmtOutput(os.Stdout, "stdout")
	fmtOutput(os.Stderr, "stderr")
	if checking {
		if os.Getenv("BACKPACK_HELPER_CHECK_FAIL") == "1" {
			os.Exit(2)
		}
		os.Exit(0)
	}
	ln, err := net.Listen("tcp", os.Getenv("BACKPACK_HELPER_LISTEN"))
	if err != nil {
		os.Exit(3)
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			os.Exit(4)
		}
		conn.Close()
	}
}

func TestHelperOutputStaysInTheDiagnosticFile(t *testing.T) {
	for _, failedCheck := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "configuration-error"}[failedCheck], func(t *testing.T) {
			dir := t.TempDir()
			output, err := os.OpenFile(filepath.Join(dir, "helper.log"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer output.Close()
			terminal, err := os.Create(filepath.Join(dir, "terminal.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			oldStderr := os.Stderr
			os.Stderr = terminal
			defer func() { os.Stderr = oldStderr }()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			env := append(os.Environ(), "BACKPACK_HELPER_OUTPUT_FIXTURE=1", "BACKPACK_HELPER_LISTEN="+listener.Addr().String())
			if failedCheck {
				env = append(env, "BACKPACK_HELPER_CHECK_FAIL=1")
			}
			logger := logrus.New()
			logger.SetOutput(io.Discard)
			ctx, cancel := context.WithTimeout(WithHelperOutput(context.Background(), output), 5*time.Second)
			defer cancel()
			h, err := startManaged(ctx, binary, "xray-server", []byte("{}"), listener.Addr().String(), env, logger, listener.Close)
			if failedCheck {
				var exited *exec.ExitError
				if h != nil || !errors.As(err, &exited) || exited.ExitCode() != 2 {
					t.Fatalf("configuration failure must still reach caller: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				h.Close()
			}
			data, err := os.ReadFile(output.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, marker := range []string{"preflight stdout", "preflight stderr"} {
				if !strings.Contains(string(data), marker) {
					t.Errorf("missing %s in diagnostic file: %s", marker, data)
				}
			}
			if !failedCheck && (!strings.Contains(string(data), "runtime stdout") || !strings.Contains(string(data), "runtime stderr")) {
				t.Errorf("child output missing: %s", data)
			}
			if stat, err := terminal.Stat(); err != nil || stat.Size() != 0 {
				t.Errorf("helper output leaked into terminal: %v", err)
			}
		})
	}
}
