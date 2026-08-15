package main

import (
	"errors"
	"fmt"
	"io"
)

// releaseAutostartConsole labels and explains the console allocated before
// main starts, then detaches the daemon from it. The Task Scheduler action still
// owns this same process for its full lifetime.
func releaseAutostartConsole(output io.Writer) error {
	return releaseAutostartConsoleWith(output, setAutostartConsoleTitle, freeAutostartConsole)
}

func releaseAutostartConsoleWith(output io.Writer, setTitle func(string) error, freeConsole func() error) error {
	var errs []error
	if err := setTitle(autostartConsoleTitle); err != nil {
		errs = append(errs, fmt.Errorf("set console title: %w", err))
	}
	if _, err := fmt.Fprintln(output, autostartConsoleBanner); err != nil {
		errs = append(errs, fmt.Errorf("write console banner: %w", err))
	}
	if err := freeConsole(); err != nil {
		errs = append(errs, fmt.Errorf("release console: %w", err))
	}
	return errors.Join(errs...)
}
