//go:build !windows

package usage

import "os"

func replaceFile(from, to string) error {
	return os.Rename(from, to)
}

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
