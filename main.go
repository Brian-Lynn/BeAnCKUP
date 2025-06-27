package main

import (
	"beanckup-cli/internal/history"
	"beanckup-cli/internal/indexer"
	"beanckup-cli/internal/restorer"
	"beanckup-cli/internal/session"
	"beanckup-cli/internal/types"
	"beanckup-cli/internal/util"
	"bufio"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	workspacePath string
	beanckupDir   string
	reader        = bufio.NewReader(os.Stdin)
)

func main() {
	fmt.Println("欢迎使用 BeanCKUP CLI！")

	// 检查是否有未完成的交付
	checkIncompleteDelivery()

	for {
		fmt.Println("\n=== 主菜单 ===")
		fmt.Println("1. 扫描和交付（备份）")
		fmt.Println("2. 恢复文件")
		fmt.Println("3. 退出程序")
		fmt.Print("\n请选择操作 (1-3): ")

		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		switch choice {
		case "1":
			handleScanAndDeliver()
		case "2":
			handleRestore()
		case "3":
			if askForConfirmation("您确定要退出吗?") {
				fmt.Println("感谢使用，再见！")
				os.Exit(0)
			}
		default:
			fmt.Println("无效选择，请输入 1-3。")
		}
	}
}

func selectWorkspace() string {
	for {
		fmt.Print("\n请输入或拖入工作区文件夹路径: ")
		path, _ := reader.ReadString('\n')
		path = strings.TrimSpace(path)
		path = strings.Trim(path, "\"") // 移除可能的引号

		if _, err := os.Stat(path); err == nil {
			return path
		}
		fmt.Printf("错误: 路径 '%s' 不存在或无法访问，请重新输入。\n", path)
	}
}

func handleScanAndDeliver() {
	// 选择工作区
	workspacePath = selectWorkspace()
	beanckupDir = filepath.Join(workspacePath, ".beanckup")

	fmt.Printf("\n已选择工作区: %s\n", workspacePath)

	// 尝试加载最新的交付计划
	plan, planErr := session.FindLatestPlan(workspacePath)

	// 尝试加载历史状态（用于判断是否存在 .beanckup 目录和历史数据）
	histState, histErr := history.LoadHistoricalState(beanckupDir)

	// 定义工作区状态
	isNewWorkspace := os.IsNotExist(histErr) || (histErr == nil && histState.MaxSessionID == 0 && len(histState.HashToRefPackage) == 0)
	hasPendingPlan := planErr == nil && plan != nil && plan.CountPending() > 0
	hasCompleteHistory := !isNewWorkspace && !hasPendingPlan // 存在历史但无待处理计划

	if hasPendingPlan {
		// 状态 2: 检测到有未完成的交付计划
		fmt.Printf("\n⚠️  发现未完成的交付任务 (会话 S%02d, 还有 %d 个包待交付)\n",
			plan.SessionID, plan.CountPending())

		// 显示交付进度
		util.DisplayDeliveryProgress(plan, filepath.Base(workspacePath))

		fmt.Println("\n选项:")
		fmt.Println("1. 继续未完成的交付")
		fmt.Println("2. 忽略并开始新的扫描")
		fmt.Print("请选择 (1-2): ")

		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		if choice == "1" {
			// 用户选择继续交付，直接进入交付流程
			fmt.Println("将继续未完成的交付...")
			// 询问交付参数（复用 askForResumeDeliveryParams 逻辑）
			deliveryParams := session.AskForResumeDeliveryParams(plan, reader)
			if deliveryParams == nil {
				fmt.Println("取消继续交付，返回主菜单。")
				return
			}
			// 执行继续交付
			session.ExecuteDeliveryLoop(plan, deliveryParams, workspacePath, beanckupDir, reader)
			return // 交付完成后返回主菜单
		}
		// 用户选择忽略，继续执行下面的扫描逻辑
		fmt.Println("将开始新的扫描...")
	} else if isNewWorkspace {
		// 状态 1: 未发现 .beanckup 目录或历史数据，认为是新工作区
		fmt.Println("首次扫描，将创建新的备份历史")
		// 初始化 histState
		histState = &types.HistoricalState{
			HashToRefPackage: make(map[string]string),
			PathToNode:       make(map[string]*types.FileNode),
			MaxSessionID:     0,
		}
	} else if hasCompleteHistory {
		// 状态 3: 检测到完整历史但无待处理计划，执行增量扫描
		fmt.Printf("检测到历史记录，最大会话ID: S%02d\n", histState.MaxSessionID)
	}

	// 检查是否有历史记录
	if histState == nil {
		log.Printf("警告: 加载历史状态失败: %v", histErr)
		histState = &types.HistoricalState{
			HashToRefPackage: make(map[string]string),
			PathToNode:       make(map[string]*types.FileNode),
			MaxSessionID:     0,
		}
	}

	fmt.Println("\n=== 开始扫描工作区 ===")

	// 扫描文件（带进度显示）
	fmt.Println("正在扫描文件...")
	idx := indexer.NewIndexer(histState)

	// 创建进度显示管理器
	progressDisplay := util.NewProgressDisplay()

	allNodes, err := idx.ScanWithProgress(workspacePath, func(progress string) {
		progressDisplay.UpdateProgress(progress)
	})

	// 完成进度显示
	progressDisplay.Finish()

	if err != nil {
		log.Printf("错误: 扫描工作区失败: %v\n", err)
		return
	}

	// 分析文件变更
	changeStats := analyzeFileChanges(allNodes)
	displayScanResults(changeStats)

	if changeStats.NewFiles == 0 && changeStats.MovedFiles == 0 {
		fmt.Println("没有新文件或移动文件需要交付。")
		return
	}

	// 询问是否开始交付
	fmt.Print("\n是否开始交付? (y/n): ")
	choice, _ := reader.ReadString('\n')
	if strings.ToLower(strings.TrimSpace(choice)) != "y" {
		fmt.Println("取消交付，返回主菜单。")
		return
	}

	// 询问交付参数
	deliveryParams := session.AskForDeliveryParams(changeStats, reader)
	if deliveryParams == nil {
		fmt.Println("取消交付。")
		return
	}

	// 创建交付计划
	newSessionID := histState.MaxSessionID + 1
	plan = session.CreatePlan(newSessionID, allNodes, deliveryParams.PackageSizeLimitMB, deliveryParams.TotalSizeLimitMB)

	if len(plan.Episodes) == 0 {
		fmt.Println("根据设置，本次扫描未计划任何交付包。")
		return
	}

	// 显示交付计划
	displayDeliveryPlan(plan, deliveryParams.TotalSizeLimitMB)

	if !askForConfirmation("是否开始执行交付?") {
		fmt.Println("取消交付。")
		return
	}

	// 执行交付
	session.ExecuteDeliveryLoop(plan, deliveryParams, workspacePath, beanckupDir, reader)
}

type ChangeStats struct {
	NewFiles     int
	MovedFiles   int
	DeletedFiles int
	TotalSize    int64
}

func analyzeFileChanges(allNodes []*types.FileNode) ChangeStats {
	stats := ChangeStats{}

	// 加载上一次扫描的历史记录
	lastManifest, err := session.LoadLastManifest(beanckupDir)
	hasHistory := err == nil && lastManifest != nil

	// 创建当前文件的路径映射，用于检测移动
	currentFilePaths := make(map[string]*types.FileNode)

	for _, node := range allNodes {
		if node.IsDirectory() {
			continue
		}

		switch node.Classification {
		case types.CLASSIFIED_NEW:
			stats.NewFiles++
			stats.TotalSize += node.Size
		case types.CLASSIFIED_REFERENCE:
			// 记录当前路径
			currentFilePaths[node.GetPath()] = node
		}
	}

	// 检测移动的文件
	stats.MovedFiles = detectMovedFiles(currentFilePaths, lastManifest)

	// 检测删除的文件
	if hasHistory {
		stats.DeletedFiles = detectDeletedFiles(currentFilePaths, lastManifest, stats.MovedFiles)
	} else {
		stats.DeletedFiles = 0
	}

	return stats
}

// detectMovedFiles 检测移动的文件
func detectMovedFiles(currentFilePaths map[string]*types.FileNode, lastManifest *types.Manifest) int {
	if lastManifest == nil {
		return 0
	}

	movedCount := 0
	for _, node := range currentFilePaths {
		if node.Classification == types.CLASSIFIED_REFERENCE && node.Reference != "" {
			// 解析引用路径
			parts := strings.SplitN(node.Reference, "/", 2)
			if len(parts) == 2 {
				originalPath := parts[1]
				currentPath := node.GetPath()

				// 如果当前路径与原始路径不同，说明文件被移动了
				if originalPath != currentPath {
					movedCount++
				}
			}
		}
	}
	return movedCount
}

// detectDeletedFiles 检测删除的文件
func detectDeletedFiles(currentFilePaths map[string]*types.FileNode, lastManifest *types.Manifest, movedFiles int) int {
	if lastManifest == nil {
		return 0
	}

	// 统计上一次扫描中的文件数（不包括目录）
	lastFileCount := 0
	for _, node := range lastManifest.Files {
		if !node.IsDirectory() {
			lastFileCount++
		}
	}

	// 统计当前扫描中的文件数（包括新文件和引用文件）
	currentFileCount := len(currentFilePaths)

	// 删除文件数 = 历史文件数 - 当前文件数 - 移动文件数
	deletedCount := lastFileCount - currentFileCount - movedFiles
	if deletedCount < 0 {
		deletedCount = 0 // 不应该出现负数
	}

	return deletedCount
}

func displayScanResults(stats ChangeStats) {
	fmt.Printf("\n=== 扫描结果 ===\n")
	fmt.Printf("新增文件: %d 个\n", stats.NewFiles)
	fmt.Printf("移动/重命名文件: %d 个\n", stats.MovedFiles)
	fmt.Printf("删除文件: %d 个\n", stats.DeletedFiles)
	fmt.Printf("总大小: %.2f MB\n", float64(stats.TotalSize)/1024/1024)
}

func displayDeliveryPlan(plan *types.Plan, totalSizeLimitMB int) {
	fmt.Printf("\n=== 交付包预览 (会话 S%02d) ===\n", plan.SessionID)
	fmt.Printf("总文件大小: %.2f MB\n", float64(plan.TotalNewSize)/1024/1024)
	fmt.Printf("交付包数量: %d\n", len(plan.Episodes))
	fmt.Println("\n交付包详情:")

	for i, episode := range plan.Episodes {
		// 根据状态显示中文描述
		var status string
		switch episode.Status {
		case types.EpisodeStatusInProgress:
			status = "正在交付"
		case types.EpisodeStatusCompleted:
			status = "已交付"
		case types.EpisodeStatusExceededLimit:
			status = "受总大小限制，待下次安排交付"
		case types.EpisodeStatusPending:
			status = "待交付"
		default:
			status = "未知状态"
		}

		// 生成包名
		workspaceName := filepath.Base(workspacePath)
		packageName := fmt.Sprintf("%s-S%02dE%02d", workspaceName, plan.SessionID, episode.ID)

		fmt.Printf("  [%d] %s - %.2f MB (%d 个文件) - %s\n",
			i+1,
			packageName,
			float64(episode.TotalSize)/1024/1024,
			len(episode.Files),
			status)
	}
}

func handleRestore() {
	fmt.Println("\n=== 文件恢复 ===")

	// 询问交付包路径
	fmt.Print("请输入交付包存放路径 (回车使用默认): ")
	deliveryPath, _ := reader.ReadString('\n')
	deliveryPath = strings.TrimSpace(deliveryPath)
	if deliveryPath == "" {
		deliveryPath = "./delivery"
	}

	// 创建恢复器并发现会话
	res, err := restorer.NewRestorer(deliveryPath)
	if err != nil {
		log.Printf("错误: 初始化恢复器失败: %v\n", err)
		return
	}

	sessions, err := res.DiscoverDeliverySessions()
	if err != nil {
		log.Printf("错误: 发现交付包失败: %v\n", err)
		return
	}

	if len(sessions) == 0 {
		fmt.Printf("在路径 '%s' 中未找到任何交付包\n", deliveryPath)
		return
	}

	// 显示发现的会话
	fmt.Printf("\n发现 %d 个备份记录:\n", len(sessions))
	for i, session := range sessions {
		fmt.Printf("  [%d] S%02d - %s\n",
			i+1,
			session.SessionID,
			session.Timestamp.Format("2006-01-02 15:04:05"))
	}

	// 选择会话
	fmt.Print("\n请选择要恢复的备份记录 (1-", len(sessions), "): ")
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)

	choiceIndex, err := strconv.Atoi(choice)
	if err != nil || choiceIndex < 1 || choiceIndex > len(sessions) {
		fmt.Println("无效选择，返回主菜单。")
		return
	}

	selectedSession := sessions[choiceIndex-1]

	// 询问密码
	fmt.Print("请输入加密密码 (如果包未加密则留空): ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)

	// 加载会话的清单文件
	fmt.Printf("\n正在加载 S%02d 的清单文件...\n", selectedSession.SessionID)
	err = res.LoadSessionManifests(selectedSession, password)
	if err != nil {
		if strings.Contains(err.Error(), "密码错误") {
			fmt.Println("检测到包需要密码，请重新输入密码:")
			fmt.Print("密码: ")
			password, _ = reader.ReadString('\n')
			password = strings.TrimSpace(password)

			// 重新尝试加载
			err = res.LoadSessionManifests(selectedSession, password)
			if err != nil {
				log.Printf("错误: 加载清单文件失败: %v\n", err)
				return
			}
		} else {
			log.Printf("错误: 加载清单文件失败: %v\n", err)
			return
		}
	}

	fmt.Printf("成功加载 %d 个交付包的清单文件\n", len(selectedSession.Manifests))

	// 询问恢复路径
	fmt.Print("请输入恢复目标路径 (回车使用默认): ")
	restorePath, _ := reader.ReadString('\n')
	restorePath = strings.TrimSpace(restorePath)
	if restorePath == "" {
		restorePath = "./restore"
	}

	if !askForConfirmation("是否开始恢复文件?") {
		fmt.Println("取消恢复。")
		return
	}

	if err = res.RestoreFromSession(selectedSession, restorePath, password); err != nil {
		log.Printf("错误: 恢复失败: %v\n", err)
	} else {
		fmt.Println("\n✓ 恢复成功！文件已存至:", restorePath)
	}
}

func askForConfirmation(prompt string) bool {
	fmt.Print(prompt + " (y/n): ")
	choice, _ := reader.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(choice)) == "y"
}

func checkIncompleteDelivery() {
	// 检查是否有未完成的交付（简化版本，主要检查当前目录下的工作区）
	currentDir, err := os.Getwd()
	if err != nil {
		return
	}

	entries, err := os.ReadDir(currentDir)
	if err != nil {
		return
	}

	var incompleteCount int
	for _, entry := range entries {
		if entry.IsDir() {
			workspacePath := filepath.Join(currentDir, entry.Name())
			beanckupDir := filepath.Join(workspacePath, ".beanckup")

			if _, err := os.Stat(beanckupDir); err == nil {
				plan, err := session.FindLatestPlan(workspacePath)
				if err == nil && plan != nil && plan.CountPending() > 0 {
					incompleteCount++
				}
			}
		}
	}

	if incompleteCount > 0 {
		fmt.Printf("提示: 发现 %d 个工作区有未完成的交付任务，您可以在\"扫描和交付\"中选择继续。\n", incompleteCount)
	}
}
