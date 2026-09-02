package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	rawStderrHelperPathEnv = "TERMP_TEST_RAW_STDERR_LOG"
	rawStderrHelperModeEnv = "TERMP_TEST_RAW_STDERR_MODE"
	rawStderrCap           = int64(32)
	managedAfterRawWrite   = "managed-after-raw\n"
)

// TestRotatingLogWriterBoundsRedirectedRawStderr claims the active generation
// grown through the raw stderr descriptor is rotated at both enforcement
// points: the next managed write during the daemon's life and the next writer
// startup. The completed raw generation is retained rather than discarded.
func TestRotatingLogWriterBoundsRedirectedRawStderr(t *testing.T) {
	if path := os.Getenv(rawStderrHelperPathEnv); path != "" {
		os.Exit(runRawStderrHelper(path, os.Getenv(rawStderrHelperModeEnv)))
	}

	raw := bytes.Repeat([]byte("r"), 4*int(rawStderrCap))
	for _, test := range []struct {
		name string
		mode string
	}{
		{name: "next managed write", mode: "managed"},
		{name: "next writer startup", mode: "startup"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "termp.log")
			command := exec.Command(os.Args[0], "-test.run=^TestRotatingLogWriterBoundsRedirectedRawStderr$")
			command.Env = append(os.Environ(),
				rawStderrHelperPathEnv+"="+path,
				rawStderrHelperModeEnv+"="+test.mode,
			)
			if err := command.Run(); err != nil {
				t.Fatalf("raw stderr helper failed: %v", err)
			}

			if test.mode == "startup" {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if info.Size() != int64(len(raw)) {
					t.Fatalf("test setup: raw stderr log size = %d, want %d before startup enforcement", info.Size(), len(raw))
				}
				writer, err := newRotatingLogWriter(path, rawStderrCap, 3)
				if err != nil {
					t.Fatal(err)
				}
				defer writer.Close()
			}

			active, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if int64(len(active)) > rawStderrCap {
				t.Fatalf("active daemon log size = %d after %s enforcement, want at most %d", len(active), test.name, rawStderrCap)
			}
			if test.mode == "managed" && string(active) != managedAfterRawWrite {
				t.Fatalf("active daemon log = %q after managed-write enforcement, want %q", active, managedAfterRawWrite)
			}

			rotated, err := os.ReadFile(path + ".1")
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(rotated, raw) {
				t.Fatalf("retained raw stderr generation has %d bytes, want the original %d bytes", len(rotated), len(raw))
			}
		})
	}
}

func runRawStderrHelper(path, mode string) int {
	writer, err := newRotatingLogWriter(path, rawStderrCap, 3)
	if err != nil {
		return 2
	}
	if err := writer.RedirectStderr(); err != nil {
		return 3
	}
	raw := bytes.Repeat([]byte("r"), 4*int(rawStderrCap))
	if written, err := os.Stderr.Write(raw); err != nil || written != len(raw) {
		return 4
	}
	if mode == "managed" {
		if written, err := writer.Write([]byte(managedAfterRawWrite)); err != nil || written != len(managedAfterRawWrite) {
			return 5
		}
	} else if mode != "startup" {
		return 6
	}
	if err := writer.Close(); err != nil {
		return 7
	}
	return 0
}

// TestRotatingLogWriterOversizedWriteReportsOriginalLength claims deliberate
// truncation consumes the complete caller record while persisting only the
// capped prefix, preserving the io.Writer contract.
func TestRotatingLogWriterOversizedWriteReportsOriginalLength(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termp.log")
	writer, err := newRotatingLogWriter(path, 32, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	record := append(bytes.Repeat([]byte("x"), 127), '\n')
	written, err := writer.Write(record)
	if err != nil {
		t.Fatalf("Write() error = %v, want nil for deliberate truncation", err)
	}
	if written != len(record) {
		t.Fatalf("Write() returned n = %d, want original input length %d", written, len(record))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 32 {
		t.Fatalf("persisted oversized record length = %d, want cap 32", len(data))
	}
}

// TestRotatingLogWriterWriteFailureReportsActualCount claims a genuine file
// write failure returns the underlying error and actual short count, even
// when the attempted record was deliberately bounded first.
func TestRotatingLogWriterWriteFailureReportsActualCount(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "termp.log")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	readOnly, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		_ = readOnly.Close()
		t.Fatal(err)
	}
	writer := &rotatingLogWriter{
		path:     path,
		maxBytes: 32,
		retained: 3,
		lockFile: lockFile,
		file:     readOnly,
	}
	defer writer.Close()

	record := bytes.Repeat([]byte("x"), 128)
	written, err := writer.Write(record)
	if err == nil {
		t.Fatal("Write() error = nil for a read-only underlying file, want a real write error")
	}
	if written != 0 {
		t.Fatalf("Write() returned n = %d after the underlying failure, want actual count 0", written)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(data) != 0 {
		t.Fatalf("read-only log contains %d bytes after failed write, want 0", len(data))
	}
}
