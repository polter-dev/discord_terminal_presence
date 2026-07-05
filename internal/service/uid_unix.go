//go:build !windows

package service

import (
	"os"
	"strconv"
)

func currentUID() string {
	return strconv.Itoa(os.Getuid())
}
