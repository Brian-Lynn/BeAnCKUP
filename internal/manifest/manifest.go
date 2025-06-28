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
func GeneratePackageName(workspaceName string, sessionID int, episodeID int) string {
	// 【核心修正】: 更新时间戳格式为 YYMMDD_HHMMSS
	timestamp := time.Now().Format("060102_150405")
	return fmt.Sprintf("%s-S%02dE%02d-%s.7z", workspaceName, sessionID, episodeID, timestamp)
}

// CreateManifest 根据给定的参数创建一个新的清单对象。
func CreateManifest(workspaceName string, sessionID int, episodeID int, packageName string, files []*types.FileNode) *types.Manifest {
	return &types.Manifest{
		WorkspaceName: workspaceName,
		SessionID:     sessionID,
		EpisodeID:     episodeID,
		Timestamp:     time.Now().UTC().Format(time.RFC3339),
		PackageName:   packageName, // 这里是基础包名，不含 .001
		Files:         files,
	}
}

// SaveManifest 将清单对象序列化为 JSON 并保存到指定目录。
func SaveManifest(manifest *types.Manifest, dir string) (string, error) {
	// 清单文件名基于包的基础名，确保与 restorer 逻辑一致
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
