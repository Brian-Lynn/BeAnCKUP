package main

import (
	"beanckup-cli/internal/history"
	"beanckup-cli/internal/indexer"
	"beanckup-cli/internal/manifest"
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
	"time"
)

var (
	reader = bufio.NewReader(os.Stdin)
)

func main() {
	fmt.Println("欢迎使用 BeanCKUP CLI！")

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

// 【新增函数】: 替换 util.DisplayDeliveryProgress 以提供更详细的信息
func displayDeliveryProgress(plan *types.Plan, workspaceName string) {
	fmt.Printf("\n=== 交付进度 (会话 S%d) ===\n", plan.SessionID)
	fmt.Printf("计划交付总大小: %.2f MB\n", float64(plan.TotalNewSize)/1024/1024)

	if len(plan.Episodes) > 0 {
		fmt.Println("\n交付包详情:")
		var deliveredSize int64
		// 从plan中获取持久化的包大小限制
		packageSizeLimitBytes := int64(plan.PackageSizeLimitMB) * 1024 * 1024

		for i, episode := range plan.Episodes {
			packageName := fmt.Sprintf("%s-S%02dE%02d", workspaceName, plan.SessionID, episode.ID)

			// 检查是否会分卷，并生成提示信息
			volumeNotice := ""
			if plan.PackageSizeLimitMB > 0 && episode.TotalSize > packageSizeLimitBytes {
				volumeNotice = " (超限，将分卷交付)"
			}

			fmt.Printf("  [%d] %s - %.2f MB (%d 个文件)%s - %s\n",
				i+1,
				packageName,
				float64(episode.TotalSize)/1024/1024,
				len(episode.Files),
				volumeNotice,
				episode.Status,
			)

			if episode.Status == types.EpisodeStatusCompleted {
				deliveredSize += episode.TotalSize
			}
		}
		fmt.Printf("\n当前已交付大小: %.2f MB\n", float64(deliveredSize)/1024/1024)
	}
}

func selectWorkspace() string {
	for {
		fmt.Print("\n请输入或拖入工作区文件夹路径: ")
		path, _ := reader.ReadString('\n')
		path = strings.TrimSpace(path)
		path = strings.Trim(path, "\"")

		if _, err := os.Stat(path); err == nil {
			return path
		}
		fmt.Printf("错误: 路径 '%s' 不存在或无法访问，请重新输入。\n", path)
	}
}

func handleScanAndDeliver() {
	workspacePath := selectWorkspace()
	workspaceName := util.GetWorkspaceName(workspacePath) // 【核心修正】: 使用新的工具函数
	beanckupDir := filepath.Join(workspacePath, ".beanckup")
	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		log.Printf("错误: 无法创建 .beanckup 目录: %v", err)
		return
	}
	// 【核心修正】: 创建后立即将其设置为隐藏
	if err := util.SetHidden(beanckupDir); err != nil {
		log.Printf("警告: 无法将 .beanckup 文件夹设置为隐藏: %v", err)
	}

	fmt.Printf("\n已选择工作区: %s\n", workspacePath)

	plan, _, err := session.FindLatestPlan(workspacePath)
	if err != nil {
		log.Printf("错误: 检查未完成任务失败: %v", err)
		return
	}

	if plan != nil && !plan.IsCompleted() {
		fmt.Printf("\n⚠️  发现未完成的交付任务 (会话 S%d, 还有 %d 个包未完成)\n",
			plan.SessionID, plan.CountUnfinished())

		// 【断点续传清理】: 清理上次未完成任务可能残留的清单文件
		for _, ep := range plan.Episodes {
			if ep.Status == types.EpisodeStatusInProgress {
				log.Printf("检测到上次交付中断，正在清理会话 S%dE%d 的残留状态...", plan.SessionID, ep.ID)
				epPackageName := manifest.GeneratePackageName(workspaceName, plan.SessionID, ep.ID)
				baseName := strings.TrimSuffix(epPackageName, ".7z")
				manifestFilename := baseName + ".json"
				manifestPath := filepath.Join(beanckupDir, manifestFilename)
				os.Remove(manifestPath)
			}
		}

		displayDeliveryProgress(plan, workspaceName) // 【核心修正】: 调用新的显示函数
		fmt.Println("\n选项:")
		fmt.Println("1. 继续未完成的交付")
		fmt.Println("2. 忽略并开始新的扫描")
		fmt.Print("请选择 (1-2): ")
		choice, _ := reader.ReadString('\n')
		choice = strings.TrimSpace(choice)
		if choice == "1" {
			fmt.Println("将继续未完成的交付...")
			params := askForResumeDeliveryParams()
			if params == nil {
				fmt.Println("取消继续交付。")
				return
			}
			executeDeliveryLoop(workspacePath, workspaceName, beanckupDir, plan, params)
			return
		}
		fmt.Println("已忽略旧任务，将开始新的扫描...")
	}

	histState, histErr := history.LoadHistoricalState(beanckupDir)
	if histErr != nil {
		log.Printf("警告: 加载历史状态失败: %v。将按首次扫描处理。", histErr)
		histState = &types.HistoricalState{
			HashToNode:   make(map[string]*types.FileNode),
			PathToNode:   make(map[string]*types.FileNode),
			MaxSessionID: 0,
		}
	}

	if histState.MaxSessionID == 0 && len(histState.PathToNode) == 0 {
		fmt.Println("首次扫描，将创建新的备份历史")
	} else {
		fmt.Printf("检测到历史记录，最大会话ID: S%d\n", histState.MaxSessionID)
	}

	fmt.Println("\n=== 开始扫描工作区 ===")
	fmt.Println("正在扫描文件...")
	idx := indexer.NewIndexer(histState)
	progressDisplay := util.NewProgressDisplay()
	allNodes, err := idx.ScanWithProgress(workspacePath, func(progress string) {
		progressDisplay.UpdateProgress(progress)
	})
	progressDisplay.Finish()
	if err != nil {
		log.Printf("错误: 扫描工作区失败: %v\n", err)
		return
	}

	newCount, movedCount, deletedCount, newSize := analyzeFileChanges(allNodes, histState)
	displayScanResults(newCount, movedCount, deletedCount, newSize)

	if newCount == 0 && movedCount == 0 {
		fmt.Println("工作区内文件无增量变化，无需交付。")
		return
	}

	if !askForConfirmation("\n是否开始交付?") {
		fmt.Println("取消交付。")
		return
	}

	params := askForDeliveryParams(newSize)
	if params == nil {
		fmt.Println("取消交付。")
		return
	}

	newSessionID := histState.MaxSessionID + 1
	newPlan := session.CreatePlan(newSessionID, allNodes, params.PackageSizeLimitMB)
	newPlan.PackageSizeLimitMB = params.PackageSizeLimitMB
	session.ApplyTotalSizeLimitToPlan(newPlan, params.TotalSizeLimitMB)

	if len(newPlan.Episodes) == 0 || newPlan.CountPending() == 0 {
		fmt.Println("根据您的设置，本次扫描未计划任何交付包。")
		return
	}

	executeDeliveryLoop(workspacePath, workspaceName, beanckupDir, newPlan, params)
}

func executeDeliveryLoop(workspacePath, workspaceName, beanckupDir string, plan *types.Plan, params *session.DeliveryParams) {
	localReader := bufio.NewReader(os.Stdin)
	currentPlan := plan

	currentParams := &session.DeliveryParams{
		DeliveryPath:       params.DeliveryPath,
		Password:           params.Password,
		CompressionLevel:   params.CompressionLevel,
		TotalSizeLimitMB:   params.TotalSizeLimitMB,
		PackageSizeLimitMB: plan.PackageSizeLimitMB,
	}

	for {
		runLimitBytes := int64(currentParams.TotalSizeLimitMB) * 1024 * 1024
		var sizeScheduledForThisRun int64
		for i := range currentPlan.Episodes {
			episode := &currentPlan.Episodes[i]
			if episode.Status == types.EpisodeStatusCompleted {
				continue
			}
			if runLimitBytes == 0 || (sizeScheduledForThisRun+episode.TotalSize <= runLimitBytes) {
				episode.Status = types.EpisodeStatusPending
				sizeScheduledForThisRun += episode.TotalSize
			} else {
				episode.Status = types.EpisodeStatusExceededLimit
			}
		}

		displayDeliveryProgress(currentPlan, workspaceName) // 【核心修正】: 调用新的显示函数

		if currentPlan.CountPending() == 0 {
			if !currentPlan.IsCompleted() {
				fmt.Println("\n根据当前总大小限制，没有可交付的任务。")
			} else {
				fmt.Println("\n所有交付任务均已完成。")
			}
		} else {
			if !askForConfirmation("是否开始执行交付?") {
				fmt.Println("取消交付。")
				return
			}
		}

		var deliveryHappened bool
		for i := range currentPlan.Episodes {
			episode := &currentPlan.Episodes[i]

			if episode.Status != types.EpisodeStatusPending {
				continue
			}
			deliveryHappened = true

			episode.Status = types.EpisodeStatusInProgress
			planFilePath, err := session.SavePlan(workspacePath, currentPlan)
			if err != nil {
				log.Printf("错误: 保存交付计划失败: %v", err)
				episode.Status = types.EpisodeStatusPending
				continue
			}
			currentPlan.StatusFilePath = planFilePath

			// --- 【核心流程重构】 ---

			// 1. 生成包名和清单对象
			episodePackageName := manifest.GeneratePackageName(workspaceName, currentPlan.SessionID, episode.ID)

			// 2. 创建一个包含所有数据文件的临时清单，用于生成 Reference
			packageManifest := manifest.CreateManifest(workspaceName, currentPlan.SessionID, episode.ID, episodePackageName, episode.Files)

			// 3. 确定引用名 (是否分卷)
			packageSizeLimitBytes := int64(currentParams.PackageSizeLimitMB) * 1024 * 1024
			willBeSplit := currentParams.PackageSizeLimitMB > 0 && episode.TotalSize > packageSizeLimitBytes

			// 4. 为清单中的新文件设置正确的引用
			var finalFilesForManifest []*types.FileNode
			for _, fileNode := range episode.Files {
				if fileNode.Reference == "" {
					refPackageName := episodePackageName
					if willBeSplit {
						refPackageName += ".001"
					}
					fileNode.Reference = fmt.Sprintf("%s/%s", refPackageName, fileNode.Path)
				}
				finalFilesForManifest = append(finalFilesForManifest, fileNode)
			}
			if episode.ID == 1 {
				finalFilesForManifest = append(finalFilesForManifest, types.FilterReferenceFiles(currentPlan.AllNodes)...)
			}
			packageManifest.Files = finalFilesForManifest

			// 5. 将最终的清单文件写入工作区的 .beanckup 目录
			manifestFilePath, err := manifest.SaveManifest(packageManifest, beanckupDir)
			if err != nil {
				log.Printf("错误: 无法在工作区创建临时清单: %v", err)
				episode.Status = types.EpisodeStatusPending
				session.SavePlan(workspacePath, currentPlan)
				continue
			}

			// 6. 准备待打包文件列表，将清单文件也作为一个节点加入
			filesToPack := make([]*types.FileNode, len(episode.Files))
			copy(filesToPack, episode.Files)

			manifestRelPath, _ := filepath.Rel(workspacePath, manifestFilePath)
			manifestInfo, _ := os.Stat(manifestFilePath)
			manifestNode := &types.FileNode{
				Path: filepath.ToSlash(manifestRelPath),
				Size: manifestInfo.Size(),
			}
			filesToPack = append(filesToPack, manifestNode)

			// 7. 调用简化的打包器
			pkg := packager.NewPackager()
			packageProgress := util.NewProgressDisplay()

			err = pkg.CreatePackage(
				currentParams.DeliveryPath,
				episodePackageName,
				workspacePath,
				filesToPack,
				currentParams.Password,
				currentParams.CompressionLevel,
				currentParams.PackageSizeLimitMB,
				func(p packager.Progress) {
					packageProgress.UpdateProgress("  > 正在处理 [%d/%d]: %d%%", i+1, len(currentPlan.Episodes), p.Percentage)
				},
			)
			packageProgress.Finish()

			// 8. 【核心修正】: 只有在打包失败时才清理临时的清单文件。
			// 成功后，清单文件必须保留在.beanckup目录作为历史记录。
			if err != nil {
				log.Printf("\n错误: 创建交付包 %s 失败: %v", episodePackageName, err)
				os.Remove(manifestFilePath) // 打包失败，清理掉这个无效的清单
				episode.Status = types.EpisodeStatusPending
				session.SavePlan(workspacePath, currentPlan)
				if !askForConfirmation("交付失败，是否继续尝试下一个包?") {
					return
				}
				continue
			}

			fmt.Printf("✓ 交付包 %s 已成功创建。\n", episodePackageName)
			episode.Status = types.EpisodeStatusCompleted
			session.SavePlan(workspacePath, currentPlan)
		}

		if deliveryHappened {
			displayDeliveryProgress(currentPlan, workspaceName) // 【核心修正】: 调用新的显示函数
		}

		if currentPlan.IsCompleted() {
			fmt.Println("\n★★★ 所有交付任务已成功完成！ ★★★")
			if currentPlan.StatusFilePath != "" {
				os.Remove(currentPlan.StatusFilePath)
				fmt.Println("✓ 进度文件已自动清理。")
			}
			return
		}

		fmt.Println("\n部分交付任务已完成。")
		fmt.Println("选项:")
		fmt.Println("1. 暂时退出程序")
		fmt.Println("2. 继续交付剩余任务")
		fmt.Print("请选择 (1-2): ")
		choice, _ := localReader.ReadString('\n')
		choice = strings.TrimSpace(choice)

		if choice == "1" {
			fmt.Println("已暂停交付，您可以稍后重新运行程序继续。")
			return
		} else if choice == "2" {
			fmt.Println("\n请重新设置交付参数以继续剩余任务:")
			resumeParams := askForResumeDeliveryParams()
			if resumeParams == nil {
				fmt.Println("取消继续交付。")
				return
			}
			currentParams.DeliveryPath = resumeParams.DeliveryPath
			currentParams.Password = resumeParams.Password
			currentParams.CompressionLevel = resumeParams.CompressionLevel
			currentParams.TotalSizeLimitMB = resumeParams.TotalSizeLimitMB
		} else {
			fmt.Println("无效选择，程序将退出。")
			return
		}
	}
}

func executeDelivery(episode *types.Episode, workspacePath, workspaceName, beanckupDir string, plan *types.Plan, params *session.DeliveryParams) {
	episode.Status = types.EpisodeStatusInProgress
	session.SavePlan(workspacePath, plan) // 更新状态

	timestamp := time.Now().Format("060102_150405")
	// 【核心修正】: 文件名使用下划线
	pkgName := fmt.Sprintf("%s_S%02dE%02d_%s.7z", workspaceName, plan.SessionID, episode.ID, timestamp)

	fmt.Printf("\n--- 正在交付 [E%02d]: %s ---\n", episode.ID, pkgName)

	// 1. 生成包名和清单对象
	episodePackageName := manifest.GeneratePackageName(workspaceName, plan.SessionID, episode.ID)

	// 2. 创建一个包含所有数据文件的临时清单，用于生成 Reference
	packageManifest := manifest.CreateManifest(workspaceName, plan.SessionID, episode.ID, episodePackageName, episode.Files)

	// 3. 确定引用名 (是否分卷)
	packageSizeLimitBytes := int64(params.PackageSizeLimitMB) * 1024 * 1024
	willBeSplit := params.PackageSizeLimitMB > 0 && episode.TotalSize > packageSizeLimitBytes

	// 4. 为清单中的新文件设置正确的引用
	var finalFilesForManifest []*types.FileNode
	for _, fileNode := range episode.Files {
		if fileNode.Reference == "" {
			refPackageName := episodePackageName
			if willBeSplit {
				refPackageName += ".001"
			}
			fileNode.Reference = fmt.Sprintf("%s/%s", refPackageName, fileNode.Path)
		}
		finalFilesForManifest = append(finalFilesForManifest, fileNode)
	}
	if episode.ID == 1 {
		finalFilesForManifest = append(finalFilesForManifest, types.FilterReferenceFiles(plan.AllNodes)...)
	}
	packageManifest.Files = finalFilesForManifest

	// 5. 将最终的清单文件写入工作区的 .beanckup 目录
	manifestFilePath, err := manifest.SaveManifest(packageManifest, beanckupDir)
	if err != nil {
		log.Printf("错误: 无法保存清单文件到历史记录: %v", err)
		// 这是一个非关键性错误，只记录日志，不中断流程
	}

	// 6. 准备待打包文件列表，将清单文件也作为一个节点加入
	filesToPack := make([]*types.FileNode, len(episode.Files))
	copy(filesToPack, episode.Files)

	manifestRelPath, _ := filepath.Rel(workspacePath, manifestFilePath)
	manifestInfo, _ := os.Stat(manifestFilePath)
	manifestNode := &types.FileNode{
		Path: filepath.ToSlash(manifestRelPath),
		Size: manifestInfo.Size(),
	}
	filesToPack = append(filesToPack, manifestNode)

	// 7. 调用简化的打包器
	pkg := packager.NewPackager()
	packageProgress := util.NewProgressDisplay()

	err = pkg.CreatePackage(
		params.DeliveryPath,
		episodePackageName,
		workspacePath,
		filesToPack,
		params.Password,
		params.CompressionLevel,
		params.PackageSizeLimitMB,
		func(p packager.Progress) {
			packageProgress.UpdateProgress("  > 正在处理 [%d/%d]: %d%%", episode.ID, len(plan.Episodes), p.Percentage)
		},
	)
	packageProgress.Finish()

	// 8. 【核心修正】: 只有在打包失败时才清理临时的清单文件。
	// 成功后，清单文件必须保留在.beanckup目录作为历史记录。
	if err != nil {
		log.Printf("\n错误: 创建交付包 %s 失败: %v", episodePackageName, err)
		os.Remove(manifestFilePath) // 打包失败，清理掉这个无效的清单
		episode.Status = types.EpisodeStatusPending
		session.SavePlan(workspacePath, plan)
		if !askForConfirmation("交付失败，是否继续尝试下一个包?") {
			return
		}
		return
	}

	fmt.Printf("✅ 交付包 %s 创建成功!\n", pkgName)

	// 9. 更新计划状态并保存
	episode.Status = types.EpisodeStatusCompleted
	if _, err := session.SavePlan(workspacePath, plan); err != nil {
		log.Printf("错误: 更新交付计划失败: %v", err)
	}
}

func analyzeFileChanges(allNodes []*types.FileNode, histState *types.HistoricalState) (newCount, movedCount, deletedCount int, newSize int64) {
	currentFilesByPath := make(map[string]*types.FileNode)
	for _, node := range allNodes {
		if !node.IsDirectory() {
			currentFilesByPath[node.Path] = node
		}
	}

	historicalFilesByPath := make(map[string]*types.FileNode)
	if histState != nil {
		for path, node := range histState.PathToNode {
			if !node.IsDirectory() {
				historicalFilesByPath[path] = node
			}
		}
	}

	for _, node := range allNodes {
		if node.IsDirectory() {
			continue
		}
		if node.Reference == "" {
			newCount++
			newSize += node.Size
		} else {
			if _, exists := historicalFilesByPath[node.Path]; !exists {
				movedCount++
			}
		}
	}

	for path, histNode := range historicalFilesByPath {
		if _, exists := currentFilesByPath[path]; !exists {
			isMoved := false
			for _, currentNode := range allNodes {
				if currentNode.Hash == histNode.Hash {
					isMoved = true
					break
				}
			}
			if !isMoved {
				deletedCount++
			}
		}
	}
	return
}

func displayScanResults(newCount, movedCount, deletedCount int, newSize int64) {
	fmt.Printf("\n=== 扫描结果 ===\n")
	fmt.Printf("新增文件: %d 个\n", newCount)
	fmt.Printf("移动/重命名文件: %d 个\n", movedCount)
	fmt.Printf("删除文件: %d 个\n", deletedCount)
	fmt.Printf("增量文件总大小: %.2f MB\n", float64(newSize)/1024/1024)
}

func askForDeliveryParams(totalNewSizeBytes int64) *session.DeliveryParams {
	params := &session.DeliveryParams{}
	localReader := bufio.NewReader(os.Stdin)

	fmt.Println("\n=== 交付参数设置 ===")
	fmt.Print("请输入交付包保存路径 (回车使用默认: ./delivery): ")
	input, _ := localReader.ReadString('\n')
	params.DeliveryPath = strings.TrimSpace(input)
	if params.DeliveryPath == "" {
		params.DeliveryPath = "./delivery"
	}

	fmt.Printf("增量文件总大小: %.2f MB\n", float64(totalNewSizeBytes)/1024/1024)

	fmt.Print("请输入本次交付的总大小限制 (MB, 回车表示无限制): ")
	input, _ = localReader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.TotalSizeLimitMB = size
	} else {
		params.TotalSizeLimitMB = 0
	}

	fmt.Print("请输入单个包大小限制 (MB, 回车表示不分割): ")
	input, _ = localReader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.PackageSizeLimitMB = size
	} else {
		params.PackageSizeLimitMB = 0 // 明确设置为0表示不分割
	}

	fmt.Print("请输入压缩级别 (0-9, 回车使用默认 0): ")
	input, _ = localReader.ReadString('\n')
	if level, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && level >= 0 && level <= 9 {
		params.CompressionLevel = level
	} else {
		params.CompressionLevel = 0
	}

	fmt.Print("请输入加密密码 (回车表示不加密): ")
	input, _ = localReader.ReadString('\n')
	params.Password = strings.TrimSpace(input)

	return params
}

func askForResumeDeliveryParams() *session.DeliveryParams {
	params := &session.DeliveryParams{}
	localReader := bufio.NewReader(os.Stdin)

	fmt.Print("请输入交付包保存路径 (回车使用默认: ./delivery): ")
	input, _ := localReader.ReadString('\n')
	params.DeliveryPath = strings.TrimSpace(input)
	if params.DeliveryPath == "" {
		params.DeliveryPath = "./delivery"
	}

	fmt.Print("请输入本次交付的总大小限制 (MB, 回车表示无限制): ")
	input, _ = localReader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.TotalSizeLimitMB = size
	} else {
		params.TotalSizeLimitMB = 0
	}

	// 移除了对 PackageSizeLimitMB 的提问，因为它已保存在 Plan 中
	// 压缩级别和密码也应在恢复时重新确认

	fmt.Print("请输入压缩级别 (0-9, 回车使用默认 0): ")
	input, _ = localReader.ReadString('\n')
	if level, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && level >= 0 && level <= 9 {
		params.CompressionLevel = level
	} else {
		params.CompressionLevel = 0
	}

	fmt.Print("请输入加密密码 (回车表示不加密): ")
	input, _ = localReader.ReadString('\n')
	params.Password = strings.TrimSpace(input)

	return params
}

func handleRestore() {
	fmt.Println("\n=== 文件恢复 ===")
	deliveryPath := askForDeliveryPath()

	// 【核心修正】: 恢复器现在只需要交付路径
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

	selectedSession, err := selectSessionToRestoreUI(sessions)
	if err != nil {
		fmt.Println(err)
		return
	}

	password := askForPassword()
	err = res.LoadSessionManifests(selectedSession, password)
	if err != nil {
		log.Printf("错误: 加载清单文件失败: %v\n", err)
		return
	}

	restorePath := askForRestorePath()
	if !confirmRestore(selectedSession, restorePath, password) {
		fmt.Println("恢复操作已取消。")
		return
	}

	if err = res.RestoreFromSession(selectedSession, restorePath, password); err != nil {
		log.Printf("错误: 恢复失败: %v\n", err)
	} else {
		workspaceName := "workspace"
		if len(selectedSession.Manifests) > 0 {
			workspaceName = util.GetWorkspaceName(selectedSession.Manifests[0].WorkspaceName)
		}
		ts := selectedSession.Timestamp.Format("060102_150405")
		recoveryDir := fmt.Sprintf("%s_S%d_%s_Recovery", workspaceName, selectedSession.SessionID, ts)
		finalRestorePath := filepath.Join(restorePath, recoveryDir)
		fmt.Println("\n✓ 恢复成功！文件已存至:", finalRestorePath)
	}
}

func selectSessionToRestoreUI(sessions []*restorer.DeliverySession) (*restorer.DeliverySession, error) {
	fmt.Printf("\n发现 %d 个备份记录:\n", len(sessions))
	for i, session := range sessions {
		// 预加载以获取时间戳等信息
		if len(session.Manifests) > 0 {
			fmt.Printf("  [%d] %s (S%d) - %s\n",
				i+1,
				session.Manifests[0].WorkspaceName,
				session.SessionID,
				session.Timestamp.Format("2006-01-02 15:04:05"))
		} else {
			// 如果没有清单，可能是旧格式或损坏的
			fmt.Printf("  [%d] S%d - (时间戳未知)\n", i+1, session.SessionID)
		}
	}
	fmt.Print("\n请选择要恢复的备份记录 (1-", len(sessions), "): ")
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)
	choiceIndex, err := strconv.Atoi(choice)
	if err != nil || choiceIndex < 1 || choiceIndex > len(sessions) {
		return nil, fmt.Errorf("无效选择，返回主菜单")
	}
	return sessions[choiceIndex-1], nil
}

func askForPassword() string {
	fmt.Print("请输入加密密码 (如果包未加密则留空): ")
	password, _ := reader.ReadString('\n')
	return strings.TrimSpace(password)
}

func confirmRestore(session *restorer.DeliverySession, restorePath, password string) bool {
	fmt.Println("\n--- 恢复确认 ---")
	if len(session.Manifests) > 0 {
		fmt.Printf("将从 %s (S%d) 恢复\n", session.Manifests[0].WorkspaceName, session.SessionID)
	}
	fmt.Printf("时间戳: %s\n", session.Timestamp.Format("2006-01-02 15:04:05"))
	fmt.Printf("恢复到: %s\n", restorePath)
	if password != "" {
		fmt.Println("解压密码: 已提供")
	}
	return askForConfirmation("是否开始恢复文件?")
}

func askForDeliveryPath() string {
	fmt.Print("请输入交付包存放路径 (回车使用默认): ")
	deliveryPath, _ := reader.ReadString('\n')
	deliveryPath = strings.TrimSpace(deliveryPath)
	if deliveryPath == "" {
		deliveryPath = "./delivery"
	}
	return deliveryPath
}

func askForRestorePath() string {
	fmt.Print("请输入恢复目标路径 (回车使用默认): ")
	restorePath, _ := reader.ReadString('\n')
	restorePath = strings.TrimSpace(restorePath)
	if restorePath == "" {
		restorePath = "./restore"
	}
	return restorePath
}

func askForConfirmation(prompt string) bool {
	fmt.Printf("%s (y/n): ", prompt)
	choice, _ := reader.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(choice)) == "y"
}
