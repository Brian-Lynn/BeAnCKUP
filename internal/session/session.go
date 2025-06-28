package session

import (
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DeliveryParams 交付参数
type DeliveryParams struct {
	DeliveryPath       string
	PackageSizeLimitMB int
	TotalSizeLimitMB   int
	CompressionLevel   int
	Password           string
}

// CreatePlan 根据扫描结果创建交付计划
func CreatePlan(sessionID int, allNodes []*types.FileNode, packageSizeLimitMB int) *types.Plan {
	plan := &types.Plan{
		SessionID: sessionID,
		Timestamp: time.Now(),
		Episodes:  []types.Episode{},
		AllNodes:  allNodes, // 存储所有扫描节点，用于后续逻辑
	}

	newFiles := types.FilterNewFiles(allNodes)

	var totalNewSize int64
	for _, node := range newFiles {
		totalNewSize += node.Size
	}
	plan.TotalNewSize = totalNewSize

	if len(newFiles) == 0 {
		return plan
	}

	// 按文件路径进行排序，而不是文件大小
	sort.Slice(newFiles, func(i, j int) bool {
		return newFiles[i].Path < newFiles[j].Path
	})

	var episodes []types.Episode
	packageSizeLimitBytes := int64(packageSizeLimitMB) * 1024 * 1024

	// 如果不分包，所有文件放入一个 episode
	if packageSizeLimitMB <= 0 {
		episodes = append(episodes, types.Episode{
			Files:     newFiles,
			TotalSize: totalNewSize,
		})
	} else {
		// 实现基于路径的顺序填充逻辑
		if len(newFiles) > 0 {
			currentEpisode := types.Episode{Files: []*types.FileNode{}, TotalSize: 0}
			for _, file := range newFiles {
				// 如果是超大文件，则它自己单独成为一个 episode
				if file.Size > packageSizeLimitBytes {
					if len(currentEpisode.Files) > 0 {
						episodes = append(episodes, currentEpisode)
					}
					episodes = append(episodes, types.Episode{
						Files:     []*types.FileNode{file},
						TotalSize: file.Size,
					})
					currentEpisode = types.Episode{Files: []*types.FileNode{}, TotalSize: 0}
					continue
				}

				// 如果当前 episode 加上新文件会超限
				if currentEpisode.TotalSize+file.Size > packageSizeLimitBytes {
					episodes = append(episodes, currentEpisode)
					currentEpisode = types.Episode{
						Files:     []*types.FileNode{file},
						TotalSize: file.Size,
					}
				} else {
					// 否则，将文件加入当前 episode
					currentEpisode.Files = append(currentEpisode.Files, file)
					currentEpisode.TotalSize += file.Size
				}
			}
			// 不要忘记循环结束后最后一个正在构建的 episode
			if len(currentEpisode.Files) > 0 {
				episodes = append(episodes, currentEpisode)
			}
		}
	}

	for i := range episodes {
		episodes[i].ID = i + 1
		episodes[i].Status = types.EpisodeStatusPending
	}

	plan.Episodes = episodes
	return plan
}

// ApplyTotalSizeLimitToPlan 根据总大小限制更新 plan 中各个 episode 的状态
func ApplyTotalSizeLimitToPlan(plan *types.Plan, totalSizeLimitMB int) {
	if totalSizeLimitMB <= 0 {
		for i := range plan.Episodes {
			if plan.Episodes[i].Status != types.EpisodeStatusCompleted {
				plan.Episodes[i].Status = types.EpisodeStatusPending
			}
		}
		return
	}

	totalSizeLimitBytes := int64(totalSizeLimitMB) * 1024 * 1024
	var cumulativeSize int64

	for i := range plan.Episodes {
		if plan.Episodes[i].Status == types.EpisodeStatusCompleted {
			cumulativeSize += plan.Episodes[i].TotalSize
		}
	}

	for i := range plan.Episodes {
		episode := &plan.Episodes[i]
		if episode.Status == types.EpisodeStatusCompleted {
			continue
		}

		if cumulativeSize+episode.TotalSize <= totalSizeLimitBytes {
			episode.Status = types.EpisodeStatusPending
		} else {
			episode.Status = types.EpisodeStatusExceededLimit
		}

		if episode.Status == types.EpisodeStatusPending {
			cumulativeSize += episode.TotalSize
		}
	}
}

// SavePlan 保存交付计划到工作区，使用临时文件和重命名确保原子性
func SavePlan(workspacePath string, plan *types.Plan) (string, error) {
	beanckupDir := filepath.Join(workspacePath, ".beanckup")
	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		return "", fmt.Errorf("无法创建 .beanckup 目录: %w", err)
	}

	workspaceName := filepath.Base(workspacePath)
	// 【核心修正】: 更新时间戳格式为 YYMMDD_HHMMSS
	timestamp := plan.Timestamp.Format("060102_150405")
	planFileName := fmt.Sprintf("Delivery_Status_%s_S%02d_%s.json", workspaceName, plan.SessionID, timestamp)
	planPath := filepath.Join(beanckupDir, planFileName)

	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化计划失败: %w", err)
	}

	tempFile, err := os.CreateTemp(beanckupDir, "plan-*.tmp")
	if err != nil {
		return "", fmt.Errorf("创建临时计划文件失败: %w", err)
	}
	defer os.Remove(tempFile.Name())

	if _, err := tempFile.Write(data); err != nil {
		tempFile.Close()
		return "", fmt.Errorf("写入临时计划文件失败: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return "", fmt.Errorf("关闭临时计划文件失败: %w", err)
	}

	if err := os.Rename(tempFile.Name(), planPath); err != nil {
		return "", fmt.Errorf("重命名计划文件失败: %w", err)
	}

	go cleanupOldStatusFiles(beanckupDir, plan.SessionID, planPath)
	return planPath, nil
}

func cleanupOldStatusFiles(dir string, currentSessionID int, currentPlanPath string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := fmt.Sprintf("Delivery_Status_%s_S%02d_", filepath.Base(filepath.Dir(dir)), currentSessionID)
	for _, file := range files {
		path := filepath.Join(dir, file.Name())
		if !file.IsDir() && strings.HasPrefix(file.Name(), prefix) && path != currentPlanPath {
			os.Remove(path)
		}
	}
}

// FindLatestPlan 查找最新的未完成的交付计划
func FindLatestPlan(workspacePath string) (*types.Plan, string, error) {
	beanckupDir := filepath.Join(workspacePath, ".beanckup")
	entries, err := os.ReadDir(beanckupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", nil
		}
		return nil, "", err
	}

	var statusFiles []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), "Delivery_Status_") && strings.HasSuffix(entry.Name(), ".json") {
			statusFiles = append(statusFiles, entry)
		}
	}

	if len(statusFiles) == 0 {
		return nil, "", nil
	}

	sort.Slice(statusFiles, func(i, j int) bool {
		infoI, _ := statusFiles[i].Info()
		infoJ, _ := statusFiles[j].Info()
		return infoI.ModTime().After(infoJ.ModTime())
	})

	for _, entry := range statusFiles {
		planPath := filepath.Join(beanckupDir, entry.Name())
		data, err := os.ReadFile(planPath)
		if err != nil {
			continue
		}
		var plan types.Plan
		if err := json.Unmarshal(data, &plan); err == nil {
			if !plan.IsCompleted() {
				return &plan, planPath, nil
			}
		}
	}
	return nil, "", nil
}

// CleanupIncompletePackages 清理与未完成的 episodes 相关的包文件
func CleanupIncompletePackages(deliveryPath string, plan *types.Plan, workspaceName string) {
	if _, err := os.Stat(deliveryPath); os.IsNotExist(err) {
		return
	}
	for i := range plan.Episodes {
		episode := &plan.Episodes[i]
		if episode.Status != types.EpisodeStatusCompleted {
			packageName := fmt.Sprintf("%s-S%02dE%02d.7z", workspaceName, plan.SessionID, episode.ID)
			packagePath := filepath.Join(deliveryPath, packageName)

			if _, err := os.Stat(packagePath); err == nil {
				fmt.Printf("清理不完整的交付包: %s\n", packageName)
				os.Remove(packagePath)
			}
			
			globPattern := packagePath + ".*"
			if files, _ := filepath.Glob(globPattern); files != nil {
				for _, f := range files {
					os.Remove(f)
				}
			}
		}
	}
}
