//go:build !windows

package util

// SetHidden 在非Windows系统上是一个空操作，因为点前缀已经处理了隐藏。
func SetHidden(path string) error {
	// 在 Unix-like 系统上，以点号开头的文件/文件夹默认被视为隐藏，无需额外操作。
	return nil
}
