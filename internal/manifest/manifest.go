package manifest

import (
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GeneratePackageName 生成符合规范的、带 .7z 后缀的唯一包文件名。
// 例如: workspace-S01E01-20250627153959.7z
func GeneratePackageName(workspaceName string, sessionID int, episodeID int) string {
	timestamp := time.Now().Format("20060102150405")
	return fmt.Sprintf("%s-S%02dE%02d-%s.7z", workspaceName, sessionID, episodeID, timestamp)
}

// CreateManifest 根据给定的参数创建一个新的清单对象。
// 它接收一个已生成的包名，以确保所有相关组件使用同一个名称。
func CreateManifest(workspaceName string, sessionID int, episodeID int, packageName string, files []*types.FileNode) *types.Manifest {
	return &types.Manifest{
		WorkspaceName: workspaceName,
		SessionID:     sessionID,
		EpisodeID:     episodeID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		PackageName:   packageName,
		Files:         files,
	}
}

// SaveManifest 将清单对象序列化为 JSON 并保存到指定目录。
// 清单文件名将与包名对应（例如 workspace-S01E01-....json）。
func SaveManifest(manifest *types.Manifest, dir string) (string, error) {
	// 从包名生成清单文件名 (e.g., "package.7z" -> "package.json")
	baseName := strings.TrimSuffix(manifest.PackageName, ".7z")
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
