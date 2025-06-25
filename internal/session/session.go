package session

import (
	"beanckup-cli/internal/manifest"
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const planFileName = "plan.json"

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

	// 检查总大小限制
	var limitedFiles []*types.FileNode
	if totalSizeLimitMB > 0 {
		totalSizeMB := totalSize / 1024 / 1024
		if totalSizeMB > int64(totalSizeLimitMB) {
			// 按修改时间排序，取最新的文件
			sort.Slice(newFiles, func(i, j int) bool {
				return newFiles[i].ModTime.After(newFiles[j].ModTime)
			})

			var limitedSize int64
			for _, file := range newFiles {
				if (limitedSize+file.Size)/1024/1024 <= int64(totalSizeLimitMB) {
					limitedFiles = append(limitedFiles, file)
					limitedSize += file.Size
				} else {
					break
				}
			}
			plan.TotalNewSize = limitedSize
		} else {
			limitedFiles = newFiles
		}
	} else {
		limitedFiles = newFiles
	}

	// 创建交付包
	if packageSizeLimitMB > 0 {
		plan.Episodes = createEpisodesBySize(limitedFiles, packageSizeLimitMB)
	} else {
		// 无大小限制，所有文件放在一个包中
		episode := types.Episode{
			ID:        1,
			TotalSize: plan.TotalNewSize,
			Files:     limitedFiles,
			Status:    types.EpisodeStatusPending,
		}
		plan.Episodes = []types.Episode{episode}
	}

	return plan
}

// createEpisodesBySize 根据大小限制创建多个交付包
func createEpisodesBySize(files []*types.FileNode, sizeLimitMB int) []types.Episode {
	var episodes []types.Episode
	var currentEpisode types.Episode
	episodeID := 1
	sizeLimit := int64(sizeLimitMB) * 1024 * 1024

	for _, file := range files {
		if currentEpisode.TotalSize+file.Size > sizeLimit && len(currentEpisode.Files) > 0 {
			// 当前包已满，保存并创建新包
			currentEpisode.ID = episodeID
			currentEpisode.Status = types.EpisodeStatusPending
			episodes = append(episodes, currentEpisode)

			episodeID++
			currentEpisode = types.Episode{}
		}

		currentEpisode.Files = append(currentEpisode.Files, file)
		currentEpisode.TotalSize += file.Size
	}

	// 添加最后一个包
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
	planPath := filepath.Join(beanckupDir, planFileName)

	data, err := os.ReadFile(planPath)
	if err != nil {
		return nil, err
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
