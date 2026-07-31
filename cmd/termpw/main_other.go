//go:build !windows

// The companion launcher is a Windows-only artifact. This stub exists only so
// the package builds on non-Windows hosts (`go build ./...`); it is never
// shipped in macOS or Linux archives.
package main

func main() {}
