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
	workspaceName := filepath.Base(workspacePath)
	beanckupDir := filepath.Join(workspacePath, ".beanckup")
	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		log.Printf("错误: 无法创建 .beanckup 目录: %v", err)
		return
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
		util.DisplayDeliveryProgress(plan, workspaceName)
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
	currentParams := params

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

		util.DisplayDeliveryProgress(currentPlan, workspaceName)

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

			episodePackageName := manifest.GeneratePackageName(workspaceName, currentPlan.SessionID, episode.ID)
			filesForPackageManifest := []*types.FileNode{}

			for _, fileNode := range episode.Files {
				if fileNode.Reference == "" {
					fileNode.Reference = fmt.Sprintf("%s/%s", episodePackageName, fileNode.Path)
				}
				filesForPackageManifest = append(filesForPackageManifest, fileNode)
			}
			if episode.ID == 1 {
				filesForPackageManifest = append(filesForPackageManifest, types.FilterReferenceFiles(currentPlan.AllNodes)...)
			}

			packageManifest := manifest.CreateManifest(workspaceName, currentPlan.SessionID, episode.ID, episodePackageName, filesForPackageManifest)
			filesToPack := episode.Files

			pkg := packager.NewPackager()
			packageProgress := util.NewProgressDisplay()
			err = pkg.CreatePackage(
				currentParams.DeliveryPath,
				packageManifest,
				workspacePath,
				filesToPack,
				currentParams.Password,
				currentParams.CompressionLevel,
				func(p packager.Progress) {
					packageProgress.UpdateProgress("  > 正在处理 [%d/%d]: %d%% - %s", i+1, len(currentPlan.Episodes), p.Percentage, p.CurrentFile)
				},
			)
			packageProgress.Finish()

			if err != nil {
				log.Printf("\n错误: 创建交付包 %s 失败: %v", packageManifest.PackageName, err)
				episode.Status = types.EpisodeStatusPending
				session.SavePlan(workspacePath, currentPlan)
				if !askForConfirmation("交付失败，是否继续尝试下一个包?") {
					return
				}
				continue
			}

			fmt.Printf("✓ 交付包 %s 已成功创建。\n", packageManifest.PackageName)
			episode.Status = types.EpisodeStatusCompleted
			if _, err := manifest.SaveManifest(packageManifest, beanckupDir); err != nil {
				log.Printf("警告: 无法保存工作区清单: %v", err)
			}
			session.SavePlan(workspacePath, currentPlan)
		}

		if deliveryHappened {
			fmt.Println("\n本轮交付完成。")
			util.DisplayDeliveryProgress(currentPlan, workspaceName)
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
			newParams := askForResumeDeliveryParams()
			if newParams == nil {
				fmt.Println("取消继续交付。")
				return
			}
			currentParams = newParams
		} else {
			fmt.Println("无效选择，程序将退出。")
			return
		}
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
		if params.TotalSizeLimitMB > 0 {
			params.PackageSizeLimitMB = params.TotalSizeLimitMB
		} else {
			params.PackageSizeLimitMB = 0
		}
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
	// 此部分未收到修改请求，保持原样
	fmt.Println("\n=== 文件恢复 ===")
	fmt.Print("请输入交付包存放路径 (回车使用默认): ")
	deliveryPath, _ := reader.ReadString('\n')
	deliveryPath = strings.TrimSpace(deliveryPath)
	if deliveryPath == "" {
		deliveryPath = "./delivery"
	}
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
	fmt.Printf("\n发现 %d 个备份记录:\n", len(sessions))
	for i, session := range sessions {
		// 修复：确保调用正确的函数名
		res.LoadSessionManifests(session, "")
		fmt.Printf("  [%d] S%d - %s\n",
			i+1,
			session.SessionID,
			session.Timestamp.Format("2006-01-02 15:04:05"))
	}
	fmt.Print("\n请选择要恢复的备份记录 (1-", len(sessions), "): ")
	choice, _ := reader.ReadString('\n')
	choice = strings.TrimSpace(choice)
	choiceIndex, err := strconv.Atoi(choice)
	if err != nil || choiceIndex < 1 || choiceIndex > len(sessions) {
		fmt.Println("无效选择，返回主菜单。")
		return
	}
	selectedSession := sessions[choiceIndex-1]
	fmt.Print("请输入加密密码 (如果包未加密则留空): ")
	password, _ := reader.ReadString('\n')
	password = strings.TrimSpace(password)
	fmt.Printf("\n正在加载 S%d 的清单文件...\n", selectedSession.SessionID)
	// 修复：确保调用正确的函数名
	err = res.LoadSessionManifests(selectedSession, password)
	if err != nil {
		log.Printf("错误: 加载清单文件失败: %v\n", err)
		return
	}
	fmt.Printf("成功加载 %d 个交付包的清单文件\n", len(selectedSession.Manifests))
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
		workspaceName := "workspace"
		if len(selectedSession.Manifests) > 0 {
			workspaceName = selectedSession.Manifests[0].WorkspaceName
		}
		ts := selectedSession.Timestamp.Format("2006-01-02 15:04:05")
		recoveryDir := fmt.Sprintf("%s_S%d_%s_Recovery", workspaceName, selectedSession.SessionID, ts)
		finalRestorePath := filepath.Join(restorePath, recoveryDir)
		fmt.Println("\n✓ 恢复成功！文件已存至:", finalRestorePath)
	}
}

func askForConfirmation(prompt string) bool {
	fmt.Print(prompt + " (y/n): ")
	choice, _ := reader.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(choice)) == "y"
}
