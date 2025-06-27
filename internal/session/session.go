package session

import (
	"beanckup-cli/internal/manifest"
	"beanckup-cli/internal/packager"
	"beanckup-cli/internal/types"
	"beanckup-cli/internal/util"
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const planFileName = "plan.json"

// DeliveryParams 交付参数
type DeliveryParams struct {
	DeliveryPath       string
	PackageSizeLimitMB int // 只在首次交付时设置，续传时不可更改
	TotalSizeLimitMB   int
	CompressionLevel   int
	Password           string
}

// CreatePlan 根据扫描结果创建交付计划
func CreatePlan(sessionID int, allNodes []*types.FileNode, packageSizeLimitMB, totalSizeLimitMB int) *types.Plan {
	plan := &types.Plan{
		SessionID:    sessionID,
		Timestamp:    time.Now(),
		AllNodes:     allNodes,
		Episodes:     []types.Episode{},
		TotalNewSize: 0,
	}

	// 筛选出新文件
	newFiles := types.FilterNewFiles(allNodes)

	// 筛选出引用文件（移动的文件）
	referenceFiles := types.FilterReferenceFiles(allNodes)

	// 如果没有新文件也没有引用文件，返回空计划
	if len(newFiles) == 0 && len(referenceFiles) == 0 {
		return plan
	}

	// 计算总大小（新文件的大小）
	var totalSize int64
	for _, file := range newFiles {
		totalSize += file.Size
	}
	plan.TotalNewSize = totalSize

	// 按修改时间排序，取最新的文件
	sort.Slice(newFiles, func(i, j int) bool {
		return newFiles[i].ModTime.After(newFiles[j].ModTime)
	})

	// 创建所有交付包（不考虑总大小限制）
	var allEpisodes []types.Episode
	if packageSizeLimitMB > 0 {
		allEpisodes = createEpisodesWithSizeLimit(newFiles, packageSizeLimitMB)
	} else {
		// 无大小限制，所有文件放在一个包中
		episode := types.Episode{
			ID:        1,
			TotalSize: plan.TotalNewSize,
			Files:     newFiles,
			Status:    types.EpisodeStatusPending,
		}
		allEpisodes = []types.Episode{episode}
	}

	// 应用总大小限制，标记超出限制的包
	if totalSizeLimitMB > 0 {
		totalSizeLimit := int64(totalSizeLimitMB) * 1024 * 1024
		var cumulativeSize int64
		var limitedEpisodes []types.Episode

		for i := range allEpisodes {
			episode := &allEpisodes[i]

			// 检查是否超出总大小限制
			if cumulativeSize+episode.TotalSize > totalSizeLimit {
				// 标记为超出限制
				episode.Status = types.EpisodeStatusExceededLimit
			} else {
				// 在限制范围内
				episode.Status = types.EpisodeStatusPending
				limitedEpisodes = append(limitedEpisodes, *episode)
			}

			cumulativeSize += episode.TotalSize
		}

		// 更新计划的总大小为实际交付的大小
		var actualDeliverySize int64
		for _, episode := range limitedEpisodes {
			actualDeliverySize += episode.TotalSize
		}
		plan.TotalNewSize = actualDeliverySize
	} else {
		// 无总大小限制，所有包都标记为待交付
		for i := range allEpisodes {
			allEpisodes[i].Status = types.EpisodeStatusPending
		}
	}

	plan.Episodes = allEpisodes
	return plan
}

// createEpisodesWithSizeLimit 创建交付包，考虑单个包大小限制
func createEpisodesWithSizeLimit(files []*types.FileNode, packageSizeLimitMB int) []types.Episode {
	var episodes []types.Episode
	var currentEpisode types.Episode
	episodeID := 1
	sizeLimit := int64(packageSizeLimitMB) * 1024 * 1024

	for _, file := range files {
		// 如果添加这个文件会超过限制，且当前包已经有文件，则完成当前包
		if currentEpisode.TotalSize+file.Size > sizeLimit && len(currentEpisode.Files) > 0 {
			// 完成当前包
			currentEpisode.ID = episodeID
			currentEpisode.Status = types.EpisodeStatusPending
			episodes = append(episodes, currentEpisode)

			episodeID++
			currentEpisode = types.Episode{}
		}

		// 添加文件到当前包
		currentEpisode.Files = append(currentEpisode.Files, file)
		currentEpisode.TotalSize += file.Size
	}

	// 处理最后一个包
	if len(currentEpisode.Files) > 0 {
		currentEpisode.ID = episodeID
		currentEpisode.Status = types.EpisodeStatusPending
		episodes = append(episodes, currentEpisode)
	}

	return episodes
}

// SavePlan 保存交付计划到工作区
func SavePlan(workspacePath string, plan *types.Plan) error {
	beanckupDir := filepath.Join(workspacePath, ".beanckup")

	// 使用新的命名格式：Delivery_Status_工作区名_Sxx_时间戳.json
	workspaceName := filepath.Base(workspacePath)
	timestamp := plan.Timestamp.Format("20060102_150405")
	planFileName := fmt.Sprintf("Delivery_Status_%s_S%02d_%s.json", workspaceName, plan.SessionID, timestamp)
	planPath := filepath.Join(beanckupDir, planFileName)

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化计划失败: %w", err)
	}

	if err := os.WriteFile(planPath, data, 0644); err != nil {
		return fmt.Errorf("保存计划失败: %w", err)
	}

	return nil
}

// FindLatestPlan 查找最新的交付计划
func FindLatestPlan(workspacePath string) (*types.Plan, error) {
	beanckupDir := filepath.Join(workspacePath, ".beanckup")

	// 查找所有Delivery_Status文件
	entries, err := os.ReadDir(beanckupDir)
	if err != nil {
		return nil, err
	}

	var statusFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "Delivery_Status_") && strings.HasSuffix(entry.Name(), ".json") {
			statusFiles = append(statusFiles, entry.Name())
		}
	}

	if len(statusFiles) == 0 {
		return nil, fmt.Errorf("未找到交付状态文件")
	}

	// 按文件名排序，取最新的（时间戳最大的）
	sort.Strings(statusFiles)
	latestFile := statusFiles[len(statusFiles)-1]

	// 读取并解析计划
	planPath := filepath.Join(beanckupDir, latestFile)
	data, err := os.ReadFile(planPath)
	if err != nil {
		return nil, fmt.Errorf("读取计划文件失败: %w", err)
	}

	var plan types.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, fmt.Errorf("解析计划失败: %w", err)
	}

	return &plan, nil
}

// CreatePackageManifest 创建随包清单（只包含当前包的文件）
func CreatePackageManifest(workspaceName string, plan *types.Plan, episode *types.Episode) *types.Manifest {
	var files []*types.FileNode

	if episode.ID == 1 {
		// E01包：包含分配给E01的新增文件 + 所有引用文件

		// 收集分配给E01的新增文件路径
		episodeNewFilePaths := make(map[string]bool)
		for _, episodeFile := range episode.Files {
			if episodeFile.Classification == types.CLASSIFIED_NEW {
				episodeNewFilePaths[episodeFile.GetPath()] = true
			}
		}

		// 从所有节点中筛选：
		// 1. 分配给E01的新增文件
		// 2. 所有引用文件
		for _, node := range plan.AllNodes {
			if node.Classification == types.CLASSIFIED_NEW {
				// 新增文件：只有分配给E01的才包含
				if episodeNewFilePaths[node.GetPath()] {
					files = append(files, node)
				}
			} else if node.Classification == types.CLASSIFIED_REFERENCE {
				// 引用文件：E01包含所有引用文件
				files = append(files, node)
			}
		}
	} else {
		// E02、E03等包：只包含分配给该包的新增文件
		episodeFilePaths := make(map[string]bool)
		for _, episodeFile := range episode.Files {
			episodeFilePaths[episodeFile.GetPath()] = true
		}

		// 只包含分配给当前包的新增文件
		for _, node := range plan.AllNodes {
			if node.Classification == types.CLASSIFIED_NEW && episodeFilePaths[node.GetPath()] {
				files = append(files, node)
			}
		}
	}

	return manifest.CreateManifestWithTimestamp(workspaceName, plan.SessionID, episode.ID, files, plan.Timestamp)
}

// CreateGlobalManifest 创建全局清单（只包含该包实际包含的文件）
func CreateGlobalManifest(workspaceName string, plan *types.Plan, episode *types.Episode) *types.Manifest {
	// 使用与CreatePackageManifest完全相同的逻辑，确保一致性
	var files []*types.FileNode

	if episode.ID == 1 {
		// E01包：包含分配给E01的新增文件 + 所有引用文件

		// 收集分配给E01的新增文件路径
		episodeNewFilePaths := make(map[string]bool)
		for _, episodeFile := range episode.Files {
			if episodeFile.Classification == types.CLASSIFIED_NEW {
				episodeNewFilePaths[episodeFile.GetPath()] = true
			}
		}

		// 从所有节点中筛选：
		// 1. 分配给E01的新增文件
		// 2. 所有引用文件
		for _, node := range plan.AllNodes {
			if node.Classification == types.CLASSIFIED_NEW {
				// 新增文件：只有分配给E01的才包含
				if episodeNewFilePaths[node.GetPath()] {
					files = append(files, node)
				}
			} else if node.Classification == types.CLASSIFIED_REFERENCE {
				// 引用文件：E01包含所有引用文件
				files = append(files, node)
			}
		}
	} else {
		// E02、E03等包：只包含分配给该包的新增文件
		episodeFilePaths := make(map[string]bool)
		for _, episodeFile := range episode.Files {
			episodeFilePaths[episodeFile.GetPath()] = true
		}

		// 只包含分配给当前包的新增文件
		for _, node := range plan.AllNodes {
			if node.Classification == types.CLASSIFIED_NEW && episodeFilePaths[node.GetPath()] {
				files = append(files, node)
			}
		}
	}

	// 使用与CreatePackageManifest相同的逻辑，确保一致性
	return manifest.CreateManifestWithTimestamp(workspaceName, plan.SessionID, episode.ID, files, plan.Timestamp)
}

// SaveManifest 保存清单到指定目录
func SaveManifest(dir string, manifestObj *types.Manifest) (string, error) {
	return manifest.SaveManifest(manifestObj, dir)
}

// LoadLastManifest 加载最新的清单文件
func LoadLastManifest(beanckupDir string) (*types.Manifest, error) {
	entries, err := os.ReadDir(beanckupDir)
	if err != nil {
		return nil, err
	}

	var jsonFiles []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") && !strings.HasPrefix(entry.Name(), "config") {
			jsonFiles = append(jsonFiles, entry.Name())
		}
	}

	if len(jsonFiles) == 0 {
		return nil, fmt.Errorf("未找到清单文件")
	}

	// 按文件名排序，取最新的
	sort.Strings(jsonFiles)
	latestFile := jsonFiles[len(jsonFiles)-1]

	// 读取并解析清单
	manifestPath := filepath.Join(beanckupDir, latestFile)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("读取清单文件失败: %w", err)
	}

	var manifest types.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析清单文件失败: %w", err)
	}

	return &manifest, nil
}

// CleanupIncompletePackages 清理不完整的压缩包
func CleanupIncompletePackages(deliveryPath string, plan *types.Plan, workspaceName string) {
	// 检查每个episode对应的包文件是否存在且完整
	for _, episode := range plan.Episodes {
		if episode.Status == types.EpisodeStatusCompleted {
			// 检查已完成的包是否真的存在
			packageName := fmt.Sprintf("%s-S%02dE%02d.zip", workspaceName, plan.SessionID, episode.ID)
			packagePath := filepath.Join(deliveryPath, packageName)

			if _, err := os.Stat(packagePath); err != nil {
				// 包文件不存在，标记为未完成
				episode.Status = types.EpisodeStatusPending
				log.Printf("警告: 已完成的包文件不存在: %s", packageName)
			}
		} else if episode.Status == types.EpisodeStatusInProgress {
			// 检查进行中的包是否完整
			packageName := fmt.Sprintf("%s-S%02dE%02d.zip", workspaceName, plan.SessionID, episode.ID)
			packagePath := filepath.Join(deliveryPath, packageName)

			if _, err := os.Stat(packagePath); err == nil {
				// 文件存在，检查是否完整（简单检查文件大小）
				if info, err := os.Stat(packagePath); err == nil {
					if info.Size() < 1024 { // 小于1KB的文件可能不完整
						// 删除不完整的文件
						os.Remove(packagePath)
						episode.Status = types.EpisodeStatusPending
						log.Printf("删除不完整的包文件: %s", packageName)
					}
				}
			} else {
				// 文件不存在，标记为未完成
				episode.Status = types.EpisodeStatusPending
			}
		}
	}
}

// CheckAndCleanupDeliveryStatus 检查并清理交付状态
func CheckAndCleanupDeliveryStatus(workspacePath, deliveryPath string) (*types.Plan, error) {
	plan, err := FindLatestPlan(workspacePath)
	if err != nil {
		return nil, err
	}

	if plan != nil {
		workspaceName := filepath.Base(workspacePath)
		CleanupIncompletePackages(deliveryPath, plan, workspaceName)
	}

	return plan, nil
}

// ExecuteDeliveryLoop 交付循环，自动处理所有包，无需用户确认
func ExecuteDeliveryLoop(plan *types.Plan, params *DeliveryParams, workspacePath, beanckupDir string, reader *bufio.Reader) {
	fmt.Println("\n=== 开始执行交付 ===")

	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		log.Fatalf("错误: 无法创建 .beanckup 目录: %v", err)
	}

	workspaceName := filepath.Base(workspacePath)
	CleanupIncompletePackages(params.DeliveryPath, plan, workspaceName)

	if err := SavePlan(workspacePath, plan); err != nil {
		log.Printf("错误: 保存交付计划失败: %v\n", err)
		return
	}

	retryCount := 0
	maxRetry := 3

DELIVERY_LOOP:
	for {
		var processedSize int64
		var hasMoreWork bool

		for i := range plan.Episodes {
			episode := &plan.Episodes[i]
			packageName := fmt.Sprintf("%s-S%02dE%02d.zip", workspaceName, plan.SessionID, episode.ID)
			if episode.Status == types.EpisodeStatusCompleted {
				continue
			}
			if episode.Status == types.EpisodeStatusExceededLimit {
				hasMoreWork = true
				continue
			}
			if episode.Status == types.EpisodeStatusPending {
				packagePath := filepath.Join(params.DeliveryPath, packageName)
				if _, err := os.Stat(packagePath); err == nil {
					os.Remove(packagePath)
					fmt.Printf("删除不完整的包文件: %s\n", packageName)
				}
			}
			episode.Status = types.EpisodeStatusInProgress
			SavePlan(workspacePath, plan)
			packageManifest := CreatePackageManifest(workspaceName, plan, episode)
			pkg := packager.NewPackager()
			progressFunc := func(p packager.Progress) {
				util.UpdateDeliveryProgress(plan, workspaceName, i, p)
			}
			err := pkg.CreatePackage(
				params.DeliveryPath,
				packageManifest,
				workspacePath,
				episode.Files,
				params.Password,
				params.CompressionLevel,
				progressFunc,
			)
			if err != nil {
				util.UpdateDeliveryStatus(plan, workspaceName, i, "打包失败，自动重试")
				log.Printf("\n错误: 创建交付包失败: %v", err)
				episode.Status = types.EpisodeStatusPending
				retryCount++
				if retryCount >= maxRetry {
					log.Printf("包 %s 连续失败 %d 次，跳过。", packageName, maxRetry)
					hasMoreWork = true
					retryCount = 0
					continue
				}
				continue DELIVERY_LOOP
			} else {
				util.UpdateDeliveryStatus(plan, workspaceName, i, "已交付")
				episode.Status = types.EpisodeStatusCompleted
				processedSize += episode.TotalSize
				globalManifest := CreateGlobalManifest(workspaceName, plan, episode)
				if _, err := SaveManifest(beanckupDir, globalManifest); err != nil {
					log.Printf("警告: 无法保存工作区清单: %v", err)
				}
			}
			SavePlan(workspacePath, plan)
		}
		if plan.IsCompleted() {
			fmt.Println("\n★★★ 所有交付任务已成功完成！ ★★★")
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
		if hasMoreWork || plan.CountPending() > 0 {
			// 自动继续，无需用户确认
			util.DisplayDeliveryProgress(plan, workspaceName)
			continue DELIVERY_LOOP
		}
	}
}

// AskForResumeDeliveryParams 只允许更改路径和总大小限制，单包大小不可更改，参数交互与表格刷新彻底分离
func AskForResumeDeliveryParams(plan *types.Plan, reader *bufio.Reader) *DeliveryParams {
	params := &DeliveryParams{}

	fmt.Println("\n=== 交付参数设置 ===")

	// 交付路径
	fmt.Print("请输入交付包保存路径 (回车使用默认): ")
	input, _ := reader.ReadString('\n')
	params.DeliveryPath = strings.TrimSpace(input)
	if params.DeliveryPath == "" {
		params.DeliveryPath = "./delivery"
	}

	// 总大小限制
	fmt.Printf("总文件大小: %.2f MB\n", float64(plan.TotalNewSize)/1024/1024)
	fmt.Print("请输入本次交付的总大小限制 (MB, 回车表示无限制): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.TotalSizeLimitMB = size
	} else {
		params.TotalSizeLimitMB = 0
	}

	// 压缩级别
	fmt.Print("请输入压缩级别 (0-9, 回车使用默认0): ")
	input, _ = reader.ReadString('\n')
	if level, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && level >= 0 && level <= 9 {
		params.CompressionLevel = level
	} else {
		params.CompressionLevel = 0
	}

	// 密码
	fmt.Print("请输入加密密码 (回车表示不加密): ")
	input, _ = reader.ReadString('\n')
	params.Password = strings.TrimSpace(input)

	// 单包大小限制不可更改，保持原计划，且单包留空时自动等于总交付量
	if len(plan.Episodes) > 0 {
		totalMB := int(plan.TotalNewSize / 1024 / 1024)
		if plan.Episodes[0].TotalSize == 0 {
			params.PackageSizeLimitMB = totalMB
		} else {
			params.PackageSizeLimitMB = int(plan.Episodes[0].TotalSize / 1024 / 1024)
		}
	}

	return params
}

// AskForDeliveryParams 交互式获取首次交付参数，单包留空时自动等于总交付量
func AskForDeliveryParams(stats interface{}, reader *bufio.Reader) *DeliveryParams {
	params := &DeliveryParams{}
	fmt.Println("\n=== 交付参数设置 ===")

	// 交付路径
	fmt.Print("请输入交付包保存路径 (回车使用默认): ")
	input, _ := reader.ReadString('\n')
	params.DeliveryPath = strings.TrimSpace(input)
	if params.DeliveryPath == "" {
		params.DeliveryPath = "./delivery"
	}

	// 包大小限制
	var totalSize float64
	if s, ok := stats.(struct{ TotalSize int64 }); ok {
		totalSize = float64(s.TotalSize)
	}
	fmt.Printf("总文件大小: %.2f MB\n", totalSize/1024/1024)
	fmt.Print("请输入单个包大小限制 (MB, 回车表示不分割): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.PackageSizeLimitMB = size
	} else {
		params.PackageSizeLimitMB = int(totalSize / 1024 / 1024) // 留空时自动等于总交付量
	}

	// 总大小限制
	fmt.Print("请输入本次交付的总大小限制 (MB, 回车表示无限制): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size > 0 {
		params.TotalSizeLimitMB = size
	} else {
		params.TotalSizeLimitMB = 0
	}

	// 压缩级别
	fmt.Print("请输入压缩级别 (0-9, 回车使用默认0): ")
	input, _ = reader.ReadString('\n')
	if level, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && level >= 0 && level <= 9 {
		params.CompressionLevel = level
	} else {
		params.CompressionLevel = 0
	}

	// 密码
	fmt.Print("请输入加密密码 (回车表示不加密): ")
	input, _ = reader.ReadString('\n')
	params.Password = strings.TrimSpace(input)

	return params
}
