package util

import (
	"beanckup-cli/internal/types"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ProgressDisplay 进度显示管理器
// 简化为单行进度显示，避免与多行输出冲突
type ProgressDisplay struct{}

// NewProgressDisplay 创建新的进度显示管理器
func NewProgressDisplay() *ProgressDisplay {
	return &ProgressDisplay{}
}

// UpdateProgress 更新进度显示。使用回车符\r将光标移到行首，并清除当前行，然后打印新消息。
// 这样可以实现单行刷新的效果。
func (pd *ProgressDisplay) UpdateProgress(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)

	// 获取终端宽度，以便正确截断和清除
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width = 80 // 获取失败时使用默认宽度
	}

	// 对消息进行 rune 切片处理，以正确处理中文字符截断
	runes := []rune(message)
	if len(runes) > width {
		if width > 3 {
			message = string(runes[:width-3]) + "..."
		} else {
			// 如果宽度太小，直接截断
			message = string(runes[:width])
		}
	}

	// 使用 \r 将光标移到行首
	// 使用空格填充来清除旧内容，然后再次使用 \r
	// 这是为了确保在所有终端上都能正确清除上一行的内容
	clearLine := strings.Repeat(" ", width)
	fmt.Printf("\r%s\r%s", clearLine, message)
}

// Finish 完成进度显示，简单地打印一个换行符，将光标移动到新行，
// 以便后续的输出不会覆盖进度条。
func (pd *ProgressDisplay) Finish() {
	fmt.Println()
}

// DisplayDeliveryProgress 重新设计，不再清屏，而是打印一个清晰的静态表格
func DisplayDeliveryProgress(plan *types.Plan, workspaceName string) {
	fmt.Printf("\n=== 交付进度 (会话 S%02d) ===\n", plan.SessionID)
	// 注意：这里显示的总大小应该是本次计划交付的所有新文件的总大小
	fmt.Printf("计划交付总大小: %.2f MB\n", float64(plan.TotalNewSize)/1024/1024)
	fmt.Println("\n交付包详情:")

	var deliveredSize int64
	for _, episode := range plan.Episodes {
		if episode.Status == types.EpisodeStatusCompleted {
			deliveredSize += episode.TotalSize
		}

		var status string
		switch episode.Status {
		case types.EpisodeStatusPending:
			status = "待交付"
		case types.EpisodeStatusInProgress:
			status = "正在交付..."
		case types.EpisodeStatusCompleted:
			status = "已交付"
		case types.EpisodeStatusExceededLimit:
			status = "超出总大小限制，等待下轮交付"
		default:
			status = "未知状态"
		}

		packageName := fmt.Sprintf("%s-S%02dE%02d", workspaceName, plan.SessionID, episode.ID)
		fmt.Printf("  [%d] %s - %.2f MB (%d 个文件) - %s\n",
			episode.ID, packageName, float64(episode.TotalSize)/1024/1024, len(episode.Files), status)
	}
	fmt.Printf("\n当前已交付大小: %.2f MB\n", float64(deliveredSize)/1024/1024)
}
