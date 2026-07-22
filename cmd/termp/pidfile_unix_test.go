//go:build !windows

package main

import (
	"os"
	"syscall"
)

func foreignOwnerFileInfo(info os.FileInfo, uid uint32) (os.FileInfo, bool) {
	return fileInfoWithSys{FileInfo: info, sys: &syscall.Stat_t{Uid: uid}}, true
}
