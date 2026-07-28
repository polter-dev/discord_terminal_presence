package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRotatingLogWriterKeepsWholeLinesAcrossOpenWriters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file handles do not share delete access, so an open second writer prevents rename-based rotation")
	}
	path := filepath.Join(t.TempDir(), "termp.log")
	first, err := newRotatingLogWriter(path, 24, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := newRotatingLogWriter(path, 24, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	for _, write := range []struct {
		writer *rotatingLogWriter
		line   string
	}{
		{writer: first, line: "writer-one-line\n"},
		{writer: second, line: "writer-two-line\n"},
		{writer: first, line: "writer-one-next\n"},
	} {
		if _, err := write.writer.Write([]byte(write.line)); err != nil {
			t.Fatal(err)
		}
	}

	for path, want := range map[string]string{
		path:        "writer-one-next\n",
		path + ".1": "writer-two-line\n",
		path + ".2": "writer-one-line\n",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Fatalf("%s = %q, want one complete line %q", filepath.Base(path), data, want)
		}
	}
}

func TestRotatingLogWriterBoundsRetainedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termp.log")
	writer, err := newRotatingLogWriter(path, 32, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()

	for line := 0; line < 12; line++ {
		if _, err := fmt.Fprintf(writer, "complete-line-%02d\n", line); err != nil {
			t.Fatal(err)
		}
	}
	for _, suffix := range []string{"", ".1", ".2", ".3"} {
		data, err := os.ReadFile(path + suffix)
		if err != nil {
			t.Fatalf("read generation %q: %v", suffix, err)
		}
		if !strings.HasSuffix(string(data), "\n") {
			t.Fatalf("generation %q ends with a partial line: %q", suffix, data)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Fatalf("unexpected generation 4: %v", err)
	}
}

func TestRotatingLogWriterCapturesPanicOnRedirectedStderr(t *testing.T) {
	if os.Getenv("TERMP_TEST_PANIC_LOG") != "" {
		writer, err := newRotatingLogWriter(os.Getenv("TERMP_TEST_PANIC_LOG"), 1<<20, 3)
		if err != nil {
			panic(err)
		}
		if err := writer.RedirectStderr(); err != nil {
			panic(err)
		}
		panic("detached-child-panic-marker")
	}

	path := filepath.Join(t.TempDir(), "termp.log")
	command := exec.Command(os.Args[0], "-test.run=^TestRotatingLogWriterCapturesPanicOnRedirectedStderr$")
	command.Env = append(os.Environ(), "TERMP_TEST_PANIC_LOG="+path)
	command.Stdout = nil
	command.Stderr = nil
	if err := command.Run(); err == nil {
		t.Fatal("panic helper exited successfully")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "panic: detached-child-panic-marker") ||
		!strings.Contains(string(data), "TestRotatingLogWriterCapturesPanicOnRedirectedStderr") {
		t.Fatalf("panic log does not contain panic and stack:\n%s", data)
	}
	t.Logf("captured detached child panic in %s:\n%s", path, data)
}
