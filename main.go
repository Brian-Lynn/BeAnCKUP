package main

import (
	"beanckup-cli/internal/history"
	"beanckup-cli/internal/indexer"
	"beanckup-cli/internal/packager"
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
		displayDeliveryProgress(plan, filepath.Base(workspacePath))

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
			deliveryParams := askForResumeDeliveryParams(plan) // 保持此函数名，但其逻辑应与原 handleResumeDelivery 中的参数询问一致
			if deliveryParams == nil {
				fmt.Println("取消继续交付，返回主菜单。")
				return
			}
			// 执行继续交付
			executeDeliveryLoop(plan, deliveryParams) // 调用新的交付循环函数
			return                                    // 交付完成后返回主菜单
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
	deliveryParams := askForDeliveryParams(changeStats)
	if deliveryParams == nil {
		fmt.Println("取消交付。")
		return
	}

	// 创建交付计划
	newSessionID := histState.MaxSessionID + 1
	plan = session.CreatePlan(newSessionID, allNodes, deliveryParams.packageSizeLimitMB, deliveryParams.totalSizeLimitMB)

	if len(plan.Episodes) == 0 {
		fmt.Println("根据设置，本次扫描未计划任何交付包。")
		return
	}

	// 显示交付计划
	displayDeliveryPlan(plan, deliveryParams.totalSizeLimitMB)

	if !askForConfirmation("是否开始执行交付?") {
		fmt.Println("取消交付。")
		return
	}

	// 执行交付
	executeDeliveryLoop(plan, deliveryParams)
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

type DeliveryParams struct {
	deliveryPath       string
	packageSizeLimitMB int
	totalSizeLimitMB   int
	compressionLevel   int
	password           string
}

func askForDeliveryParams(stats ChangeStats) *DeliveryParams {
	params := &DeliveryParams{}

	fmt.Println("\n=== 交付参数设置 ===")

	// 交付路径
	fmt.Print("请输入交付包保存路径 (回车使用默认): ")
	input, _ := reader.ReadString('\n')
	params.deliveryPath = strings.TrimSpace(input)
	if params.deliveryPath == "" {
		params.deliveryPath = "./delivery"
	}

	// 包大小限制
	fmt.Printf("总文件大小: %.2f MB\n", float64(stats.TotalSize)/1024/1024)
	fmt.Print("请输入单个包大小限制 (MB, 回车表示不分割): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.packageSizeLimitMB = size
	} else {
		params.packageSizeLimitMB = 0
	}

	// 总大小限制
	fmt.Print("请输入本次交付的总大小限制 (MB, 回车表示无限制): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.totalSizeLimitMB = size
	} else {
		params.totalSizeLimitMB = 0
	}

	// 压缩级别
	fmt.Print("请输入压缩级别 (0-9, 回车使用默认0): ")
	input, _ = reader.ReadString('\n')
	if level, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && level >= 0 && level <= 9 {
		params.compressionLevel = level
	} else {
		params.compressionLevel = 0
	}

	// 密码
	fmt.Print("请输入加密密码 (回车表示不加密): ")
	input, _ = reader.ReadString('\n')
	params.password = strings.TrimSpace(input)

	return params
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

func executeDeliveryLoop(plan *types.Plan, params *DeliveryParams) {
	fmt.Println("\n=== 开始执行交付 ===")

	// 现在才创建 .beanckup 目录
	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		log.Fatalf("错误: 无法创建 .beanckup 目录: %v", err)
	}

	// 清理不完整的压缩包
	workspaceName := filepath.Base(workspacePath)
	session.CleanupIncompletePackages(params.deliveryPath, plan, workspaceName)

	// 保存计划
	if err := session.SavePlan(workspacePath, plan); err != nil {
		log.Printf("错误: 保存交付计划失败: %v\n", err)
		return
	}

	// 显示初始交付包状态
	displayDeliveryProgress(plan, workspaceName)

	// 交付循环
	for {
		var processedSize int64
		var hasMoreWork bool

		for i := range plan.Episodes {
			episode := &plan.Episodes[i]
			if episode.Status == types.EpisodeStatusCompleted {
				continue
			}

			// 只处理状态为 PENDING 的包，跳过超出总大小限制的包
			if episode.Status == types.EpisodeStatusExceededLimit {
				hasMoreWork = true
				continue
			}

			// 检查是否存在不完整的包文件，如果存在则删除
			if episode.Status == types.EpisodeStatusPending {
				packageName := fmt.Sprintf("%s-S%02dE%02d.zip", workspaceName, plan.SessionID, episode.ID)
				packagePath := filepath.Join(params.deliveryPath, packageName)
				if _, err := os.Stat(packagePath); err == nil {
					// 删除不完整文件，确保重新打包
					os.Remove(packagePath)
					fmt.Printf("删除不完整的包文件: %s\n", packageName)
				}
			}

			episode.Status = types.EpisodeStatusInProgress
			session.SavePlan(workspacePath, plan)

			// 创建清单
			packageManifest := session.CreatePackageManifest(workspaceName, plan, episode)

			// 先创建交付包，成功后再保存工作区manifest
			//fmt.Printf("\n正在创建交付包: %s (%d/%d)\n",
			//	packageManifest.PackageName, i+1, len(plan.Episodes))

			// 创建打包器并执行
			pkg := packager.NewPackager()

			progressFunc := func(p packager.Progress) {
				updateDeliveryProgress(plan, workspaceName, i, p)
			}

			err := pkg.CreatePackage(
				params.deliveryPath,
				packageManifest,
				workspacePath,
				episode.Files,
				params.password,
				params.compressionLevel,
				progressFunc,
			)

			if err != nil {
				updateDeliveryStatus(plan, workspaceName, i, "打包失败")
				log.Printf("\n错误: 创建交付包失败: %v", err)
				episode.Status = types.EpisodeStatusPending
				hasMoreWork = true
			} else {
				updateDeliveryStatus(plan, workspaceName, i, "已交付")
				episode.Status = types.EpisodeStatusCompleted
				processedSize += episode.TotalSize

				// 交付包创建成功后，保存与交付包内完全一致的manifest到工作区
				globalManifest := session.CreateGlobalManifest(workspaceName, plan, episode)
				if _, err := session.SaveManifest(beanckupDir, globalManifest); err != nil {
					log.Printf("警告: 无法保存工作区清单: %v", err)
				} else {
					//fmt.Printf("✓ 工作区清单已更新: %s\n", globalManifest.PackageName)
				}
			}

			session.SavePlan(workspacePath, plan)
		}

		// 检查是否所有任务都已完成
		if plan.IsCompleted() {
			fmt.Println("\n★★★ 所有交付任务已成功完成！ ★★★")

			// 清理进度文件（使用新的命名格式）
			timestamp := plan.Timestamp.Format("20060102_150405")
			statusFileName := fmt.Sprintf("Delivery_Status_%s_S%02d_%s.json", workspaceName, plan.SessionID, timestamp)
			statusPath := filepath.Join(beanckupDir, statusFileName)

			if err := os.Remove(statusPath); err != nil {
				log.Printf("警告: 无法删除进度文件: %v", err)
			} else {
				fmt.Println("✓ 进度文件已自动清理")
			}
			return
		}

		// 检查是否还有未完成的任务
		if hasMoreWork || plan.CountPending() > 0 {
			fmt.Println("\n部分交付任务已完成。")
			fmt.Println("选项:")
			fmt.Println("1. 暂时退出程序")
			fmt.Println("2. 继续交付剩余任务")
			fmt.Print("请选择 (1-2): ")

			choice, _ := reader.ReadString('\n')
			choice = strings.TrimSpace(choice)

			if choice == "1" {
				fmt.Println("已退出交付流程，您可以稍后重新运行继续交付。")
				return
			} else if choice == "2" {
				// 重新询问交付参数
				newParams := askForResumeDeliveryParams(plan)
				if newParams == nil {
					fmt.Println("取消继续交付，返回主菜单。")
					return
				}
				// 更新参数并继续循环
				params = newParams
				continue
			} else {
				fmt.Println("无效选择，将退出交付流程。")
				return
			}
		}
	}
}

// displayDeliveryProgress 显示交付进度表格
func displayDeliveryProgress(plan *types.Plan, workspaceName string) {
	// 清屏并重新显示
	fmt.Print("\033[2J\033[H") // 清屏并移动光标到顶部

	fmt.Printf("=== 交付进度 (会话 S%02d) ===\n", plan.SessionID)
	fmt.Printf("总文件大小: %.2f MB\n", float64(plan.TotalNewSize)/1024/1024)
	fmt.Println("\n交付包详情:")

	for i, episode := range plan.Episodes {
		status := "待交付"

		if episode.Status == types.EpisodeStatusInProgress {
			status = "正在交付"
		} else if episode.Status == types.EpisodeStatusCompleted {
			status = "已交付"
		} else if episode.Status == types.EpisodeStatusExceededLimit {
			status = "超出总大小限制，等待下轮交付"
		}

		// 生成包名
		packageName := fmt.Sprintf("%s-S%02dE%02d", workspaceName, plan.SessionID, episode.ID)

		fmt.Printf("  [%d] %s - %.2f MB (%d 个文件) - %s\n",
			i+1,
			packageName,
			float64(episode.TotalSize)/1024/1024,
			len(episode.Files),
			status)
	}
}

// updateDeliveryProgress 实时刷新某一包的进度
func updateDeliveryProgress(plan *types.Plan, workspaceName string, episodeIndex int, progress packager.Progress) {
	currentLine := 4 + episodeIndex // 标题3行+1空行+包序号
	fmt.Printf("\033[%d;0H", currentLine)
	fmt.Print("\033[K")
	e := &plan.Episodes[episodeIndex]
	packageName := fmt.Sprintf("%s-S%02dE%02d", workspaceName, plan.SessionID, e.ID)
	status := fmt.Sprintf("正在交付 %d%%", progress.Percentage)
	if progress.CurrentFile != "" {
		parts := strings.Split(progress.CurrentFile, " ")
		if len(parts) >= 2 {
			status = fmt.Sprintf("正在交付 %d%% (%s/%d)", progress.Percentage, parts[1], len(e.Files))
		}
	}
	fmt.Printf("  [%d] %s - %.2f MB (%d 个文件) - %s\n",
		episodeIndex+1, packageName, float64(e.TotalSize)/1024/1024, len(e.Files), status)
}

// updateDeliveryStatus 刷新某一包的最终状态
func updateDeliveryStatus(plan *types.Plan, workspaceName string, episodeIndex int, status string) {
	currentLine := 4 + episodeIndex
	fmt.Printf("\033[%d;0H", currentLine)
	fmt.Print("\033[K")
	e := &plan.Episodes[episodeIndex]
	packageName := fmt.Sprintf("%s-S%02dE%02d", workspaceName, plan.SessionID, e.ID)
	fmt.Printf("  [%d] %s - %.2f MB (%d 个文件) - %s\n",
		episodeIndex+1, packageName, float64(e.TotalSize)/1024/1024, len(e.Files), status)
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

func askForResumeDeliveryParams(plan *types.Plan) *DeliveryParams {
	params := &DeliveryParams{}

	fmt.Println("\n=== 交付参数设置 ===")

	// 交付路径
	fmt.Print("请输入交付包保存路径 (回车使用默认): ")
	input, _ := reader.ReadString('\n')
	params.deliveryPath = strings.TrimSpace(input)
	if params.deliveryPath == "" {
		params.deliveryPath = "./delivery"
	}

	// 包大小限制
	fmt.Printf("总文件大小: %.2f MB\n", float64(plan.TotalNewSize)/1024/1024)
	fmt.Print("请输入单个包大小限制 (MB, 回车表示不分割): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.packageSizeLimitMB = size
	} else {
		params.packageSizeLimitMB = 0
	}

	// 总大小限制
	fmt.Print("请输入本次交付的总大小限制 (MB, 回车表示无限制): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.totalSizeLimitMB = size
	} else {
		params.totalSizeLimitMB = 0
	}

	// 压缩级别
	fmt.Print("请输入压缩级别 (0-9, 回车使用默认0): ")
	input, _ = reader.ReadString('\n')
	if level, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && level >= 0 && level <= 9 {
		params.compressionLevel = level
	} else {
		params.compressionLevel = 0
	}

	// 密码
	fmt.Print("请输入加密密码 (回车表示不加密): ")
	input, _ = reader.ReadString('\n')
	params.password = strings.TrimSpace(input)

	return params
}
