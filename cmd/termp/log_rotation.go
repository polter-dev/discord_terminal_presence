package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	detachedLogMaxBytes = 1 << 20
	detachedLogRetained = 3
)

type rotatingLogWriter struct {
	path     string
	maxBytes int64
	retained int
	lockFile *os.File
	file     *os.File
	stderr   bool
	mu       sync.Mutex
}

func newRotatingLogWriter(path string, maxBytes int64, retained int) (*rotatingLogWriter, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid daemon log size cap %d", maxBytes)
	}
	if retained < 1 {
		return nil, fmt.Errorf("invalid retained daemon log count %d", retained)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create daemon log directory: %w", err)
	}
	// MkdirAll is a no-op on an already-existing directory, so it never
	// tightens a directory that was created earlier under a looser umask or
	// by an older version (issue #561).
	if err := tightenLogDirectoryPermissions(dir); err != nil {
		return nil, fmt.Errorf("tighten daemon log directory permissions: %w", err)
	}
	lockFile, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon log rotation lock: %w", err)
	}
	writer := &rotatingLogWriter{
		path:     path,
		maxBytes: maxBytes,
		retained: retained,
		lockFile: lockFile,
	}
	if err := writer.withRotationLock(func() error {
		return writer.openCurrentLocked()
	}); err != nil {
		_ = lockFile.Close()
		return nil, err
	}
	return writer, nil
}

func (w *rotatingLogWriter) Write(line []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	written := 0
	err := w.withRotationLock(func() error {
		var rotationErr error
		if err := w.openCurrentLocked(); err != nil {
			return err
		}
		info, err := w.file.Stat()
		if err != nil {
			return err
		}
		if info.Size() > 0 && info.Size()+int64(len(line)) > w.maxBytes {
			if err := w.rotateLocked(); err != nil {
				rotationErr = err
				if reopenErr := w.openCurrentLocked(); reopenErr != nil {
					return errors.Join(err, reopenErr)
				}
			}
		}
		written, err = w.file.Write(line)
		return errors.Join(rotationErr, err)
	})
	return written, err
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	var fileErr error
	if w.file != nil {
		fileErr = w.file.Close()
		w.file = nil
	}
	lockErr := w.lockFile.Close()
	return errors.Join(fileErr, lockErr)
}

// RedirectStderr keeps runtime panic output on the same bounded log stream.
// Rotation rebinds stderr to the newly opened current generation.
func (w *rotatingLogWriter) RedirectStderr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.openCurrentLocked(); err != nil {
		return err
	}
	if err := redirectStderr(w.file); err != nil {
		return fmt.Errorf("redirect daemon stderr: %w", err)
	}
	w.stderr = true
	info, err := w.file.Stat()
	if err != nil {
		return err
	}
	if info.Size() >= w.maxBytes {
		if err := w.withRotationLock(w.rotateLocked); err != nil {
			return fmt.Errorf("rotate daemon log after redirecting stderr: %w", err)
		}
	}
	return nil
}

func (w *rotatingLogWriter) withRotationLock(fn func() error) error {
	if err := lockLogRotation(w.lockFile); err != nil {
		return err
	}
	defer unlockLogRotation(w.lockFile)
	return fn()
}

func (w *rotatingLogWriter) openCurrentLocked() error {
	if w.file != nil {
		currentInfo, currentErr := w.file.Stat()
		pathInfo, pathErr := os.Stat(w.path)
		if currentErr == nil && pathErr == nil && os.SameFile(currentInfo, pathInfo) {
			return nil
		}
		_ = w.file.Close()
		w.file = nil
	}
	file, err := openLogFile(w.path, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	w.file = file
	if w.stderr {
		if err := redirectStderr(file); err != nil {
			_ = file.Close()
			w.file = nil
			return fmt.Errorf("redirect daemon stderr after log rotation: %w", err)
		}
	}
	return nil
}

func (w *rotatingLogWriter) rotateLocked() error {
	if w.file != nil {
		if err := w.file.Close(); err != nil {
			w.file = nil
			return err
		}
		w.file = nil
	}
	oldest := fmt.Sprintf("%s.%d", w.path, w.retained)
	if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
		return err
	}
	for generation := w.retained - 1; generation >= 1; generation-- {
		source := fmt.Sprintf("%s.%d", w.path, generation)
		destination := fmt.Sprintf("%s.%d", w.path, generation+1)
		if err := os.Rename(source, destination); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if err := os.Rename(w.path, w.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.openCurrentLocked()
}
