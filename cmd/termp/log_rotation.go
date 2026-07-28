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
	mu       sync.Mutex
}

func newRotatingLogWriter(path string, maxBytes int64, retained int) (*rotatingLogWriter, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("invalid daemon log size cap %d", maxBytes)
	}
	if retained < 1 {
		return nil, fmt.Errorf("invalid retained daemon log count %d", retained)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon log directory: %w", err)
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
		if err := writer.openCurrentLocked(); err != nil {
			return err
		}
		info, err := writer.file.Stat()
		if err != nil {
			return err
		}
		if info.Size() >= writer.maxBytes {
			return writer.rotateLocked()
		}
		return nil
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
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open daemon log: %w", err)
	}
	w.file = file
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
