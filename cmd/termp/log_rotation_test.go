package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRotatingLogWriterKeepsWholeLinesAcrossOpenWriters(t *testing.T) {
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
