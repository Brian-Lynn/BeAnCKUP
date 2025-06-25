//go:build !windows

package util

import (
	"os"
	"time"
)

// GetCreationTime 为非 Windows 系统提供回退。
// 对于 Linux/macOS 等系统，没有统一标准的创建时间，因此使用修改时间作为替代。
func GetCreationTime(path string) (time.Time, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}
