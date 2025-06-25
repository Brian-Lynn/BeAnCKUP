package manifest

import (
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GenerateManifestName 生成符合规范的清单和包名称。
func GenerateManifestName(workspaceName string, sessionID int, episodeID int) string {
	timestamp := time.Now().Format("20060102150405")
	return fmt.Sprintf("%s-S%02dE%02d-%s", workspaceName, sessionID, episodeID, timestamp)
}

// CreateManifest 根据扫描结果创建一个新的清单对象。
func CreateManifest(workspaceName string, sessionID int, episodeID int, allNodes []*types.FileNode) *types.Manifest {
	baseName := GenerateManifestName(workspaceName, sessionID, episodeID)

	manifest := &types.Manifest{
		WorkspaceName: workspaceName,
		SessionID:     sessionID,
		EpisodeID:     episodeID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		PackageName:   baseName + ".7z",
		Files:         allNodes,
	}
	return manifest
}

// CreateManifestWithTimestamp 根据扫描结果创建一个新的清单对象，使用指定的时间戳。
func CreateManifestWithTimestamp(workspaceName string, sessionID int, episodeID int, allNodes []*types.FileNode, timestamp time.Time) *types.Manifest {
	baseName := GenerateManifestName(workspaceName, sessionID, episodeID)

	manifest := &types.Manifest{
		WorkspaceName: workspaceName,
		SessionID:     sessionID,
		EpisodeID:     episodeID,
		Timestamp:     timestamp.Format(time.RFC3339),
		PackageName:   baseName + ".7z",
		Files:         allNodes,
	}
	return manifest
}

// SaveManifest 将清单对象序列化为 JSON 并保存到指定目录。
func SaveManifest(manifest *types.Manifest, dir string) (string, error) {
	baseName := manifest.PackageName[:len(manifest.PackageName)-3] // 移除 .7z
	manifestFilename := baseName + ".json"
	filePath := filepath.Join(dir, manifestFilename)

	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("无法序列化清单: %w", err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("无法写入清单文件: %w", err)
	}

	return filePath, nil
}
