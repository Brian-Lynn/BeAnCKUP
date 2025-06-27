package util

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

// PrintProgress 在单行中打印进度信息，并处理终端宽度和残留字符问题。
// message: 要显示的消息内容。
func PrintProgress(message string) {
	// 获取终端宽度
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		// 获取失败时使用默认宽度
		width = 80
	}

	// 如果消息过长，则截断
	if len(message) > width {
		message = message[:width-3] + "..."
	}

	// 使用空格填充剩余部分，以清除残留字符
	line := fmt.Sprintf("%*s", width, message)

	// 使用回车符 \r 将光标移到行首，然后打印新行
	fmt.Printf("\r%s", line)
}
