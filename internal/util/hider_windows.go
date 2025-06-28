//go:build windows

package util

import (
	"path/filepath"
	"syscall"
)

// SetHidden 在Windows上为文件或目录设置“隐藏”属性。
func SetHidden(path string) error {
	// 获取文件的UTF16指针
	ptr, err := syscall.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return err
	}
	// 获取当前属性
	attrs, err := syscall.GetFileAttributes(ptr)
	if err != nil {
		return err
	}
	// 添加隐藏属性并设置
	return syscall.SetFileAttributes(ptr, attrs|syscall.FILE_ATTRIBUTE_HIDDEN)
}
