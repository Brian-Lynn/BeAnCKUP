package util

import (
	"fmt"
	"os"
	"strings"
)

// ProgressDisplay 进度显示管理器
type ProgressDisplay struct {
	lastLineCount int // 上次显示的行数
}

// NewProgressDisplay 创建新的进度显示管理器
func NewProgressDisplay() *ProgressDisplay {
	return &ProgressDisplay{lastLineCount: 0}
}

// UpdateProgress 更新进度显示，处理终端宽度和换行
func (pd *ProgressDisplay) UpdateProgress(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	pd.displayMessage(message)
}

// displayMessage 显示消息，处理多行显示
func (pd *ProgressDisplay) displayMessage(message string) {
	// 获取终端宽度（简化处理）
	width := pd.getTerminalWidth()

	// 清除上次显示的行
	pd.clearLastLines()

	// 处理消息换行
	lines := pd.wrapMessage(message, width)

	// 显示新消息
	for _, line := range lines {
		fmt.Println(line)
	}

	// 记录当前行数
	fmt.Printf("DEBUG: Current progress lines: %d\n", len(lines))
	pd.lastLineCount = len(lines)
}

// clearLastLines 清除上次显示的行
func (pd *ProgressDisplay) clearLastLines() {
	if pd.lastLineCount > 0 {
		// 向上移动光标
		fmt.Printf("\033[%dA", pd.lastLineCount)
		// 清除从光标到行尾的内容
		for i := 0; i < pd.lastLineCount; i++ {
			fmt.Print("\r\033[K")
			if i < pd.lastLineCount-1 {
				fmt.Print("\033[B") // 向下移动一行
			}
		}
		if pd.lastLineCount > 1 {
			// 回到第一行
			fmt.Printf("\033[%dA", pd.lastLineCount)
		}
	}
}

// wrapMessage 将消息按终端宽度换行
func (pd *ProgressDisplay) wrapMessage(message string, width int) []string {
	var lines []string
	words := strings.Fields(message)
	var currentLine strings.Builder

	for _, word := range words {
		// 如果当前行加上新单词会超过宽度，开始新行
		if currentLine.Len()+len(word)+1 > width {
			if currentLine.Len() > 0 {
				lines = append(lines, currentLine.String())
				currentLine.Reset()
			}
		}

		// 添加单词到当前行
		if currentLine.Len() > 0 {
			currentLine.WriteString(" ")
		}
		currentLine.WriteString(word)
	}

	// 添加最后一行
	if currentLine.Len() > 0 {
		lines = append(lines, currentLine.String())
	}

	return lines
}

// getTerminalWidth 获取终端宽度（简化实现）
func (pd *ProgressDisplay) getTerminalWidth() int {
	// 尝试获取COLUMNS环境变量
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if width := atoi(cols); width > 0 {
			return width
		}
	}

	// 尝试获取TERM环境变量来判断是否为终端
	if term := os.Getenv("TERM"); term != "" {
		return 80 // 终端默认宽度
	}

	// Windows 系统，尝试获取控制台宽度
	if isWindows() {
		// 简化处理，返回默认宽度
		return 80
	}

	return 80 // 默认宽度
}

// isWindows 检查是否为Windows系统
func isWindows() bool {
	return os.PathSeparator == '\\' && os.PathListSeparator == ';'
}

// atoi 简单的字符串转整数函数
func atoi(s string) int {
	var result int
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			result = result*10 + int(ch-'0')
		} else {
			break
		}
	}
	return result
}

// Finish 完成进度显示，清除进度行
func (pd *ProgressDisplay) Finish() {
	pd.clearLastLines()
	pd.lastLineCount = 0
}
