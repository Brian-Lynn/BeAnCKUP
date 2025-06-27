package util

import (
	"beanckup-cli/internal/types"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// ProgressDisplay 进度显示管理器
type ProgressDisplay struct{}

// NewProgressDisplay 创建新的进度显示管理器
func NewProgressDisplay() *ProgressDisplay {
	return &ProgressDisplay{}
}

// UpdateProgress 更新进度显示。
func (pd *ProgressDisplay) UpdateProgress(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	width, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		width = 80
	}
	runes := []rune(message)
	if len(runes) > width {
		if width > 3 {
			message = string(runes[:width-3]) + "..."
		} else {
			message = string(runes[:width])
		}
	}
	clearLine := strings.Repeat(" ", width)
	fmt.Printf("\r%s\r%s", clearLine, message)
}

// Finish 完成进度显示。
func (pd *ProgressDisplay) Finish() {
	fmt.Println()
}

// DisplayDeliveryProgress 打印交付进度表格
func DisplayDeliveryProgress(plan *types.Plan, workspaceName string) {
	fmt.Printf("\n=== 交付进度 (会话 S%d) ===\n", plan.SessionID)
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
		
		// 修复：使用 %d，以正确显示超过两位数的编号
		packageName := fmt.Sprintf("%s-S%dE%d", workspaceName, plan.SessionID, episode.ID)
		fmt.Printf("  [%d] %s - %.2f MB (%d 个文件) - %s\n",
			episode.ID, packageName, float64(episode.TotalSize)/1024/1024, len(episode.Files), status)
	}
	fmt.Printf("\n当前已交付大小: %.2f MB\n", float64(deliveredSize)/1024/1024)
}
