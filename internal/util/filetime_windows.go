//go:build windows

package util

import (
	"os"
	"syscall"
	"time"
)

// GetCreationTime 获取文件的创建时间 (仅Windows)。
func GetCreationTime(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	stat := fi.Sys().(*syscall.Win32FileAttributeData)
	return time.Unix(0, stat.CreationTime.Nanoseconds()), nil
}
