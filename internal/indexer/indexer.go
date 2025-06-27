package indexer

import (
	"beanckup-cli/internal/types"
	"beanckup-cli/internal/util"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Indexer 负责扫描工作区并根据历史记录对文件进行分类。
type Indexer struct {
	history *types.HistoricalState
}

// NewIndexer 创建一个新的 Indexer 实例。
func NewIndexer(history *types.HistoricalState) *Indexer {
	return &Indexer{history: history}
}

// ScanWithProgress 递归扫描工作区路径，支持进度回调
func (idx *Indexer) ScanWithProgress(workspacePath string, progressCallback func(string)) ([]*types.FileNode, error) {
	var allNodes []*types.FileNode
	var filesToScan []string

	filepath.Walk(workspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if strings.Contains(path, ".beanckup") || strings.EqualFold(info.Name(), "Thumbs.db") {
				return nil
			}
			filesToScan = append(filesToScan, path)
		}
		return nil
	})

	totalFiles := len(filesToScan)
	processedFiles := 0

	err := filepath.Walk(workspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".beanckup" {
			return filepath.SkipDir
		}
		if path == workspacePath {
			return nil
		}
		if !info.IsDir() && strings.EqualFold(info.Name(), "Thumbs.db") {
			return nil
		}

		relPath, err := filepath.Rel(workspacePath, path)
		if err != nil {
			return fmt.Errorf("无法获取相对路径: %w", err)
		}
		relPath = filepath.ToSlash(relPath)

		node := idx.classifyFile(workspacePath, relPath, info)
		allNodes = append(allNodes, node)

		if !info.IsDir() {
			processedFiles++
			progress := fmt.Sprintf("扫描进度: %d/%d 文件 (%.1f%%) - %s",
				processedFiles, totalFiles, float64(processedFiles)/float64(totalFiles)*100, relPath)
			progressCallback(progress)
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return allNodes, nil
}

// classifyFile 对单个文件或目录进行分类，并确定其引用关系。
func (idx *Indexer) classifyFile(workspaceRoot, relPath string, info os.FileInfo) *types.FileNode {
	if info.IsDir() {
		return &types.FileNode{Dir: relPath, ModTime: info.ModTime().UTC()}
	}

	node := &types.FileNode{Path: relPath, Size: info.Size(), ModTime: info.ModTime().UTC()}
	cTime, err := util.GetCreationTime(filepath.Join(workspaceRoot, relPath))
	if err == nil {
		node.CreateTime = cTime.UTC()
	}

	// 1. 五元预筛
	if lastState, ok := idx.history.PathToNode[relPath]; ok &&
		!lastState.IsDirectory() && lastState.Size == node.Size &&
		lastState.ModTime.Equal(node.ModTime) && lastState.CreateTime.Equal(node.CreateTime) {
		node.Hash = lastState.Hash
		node.Reference = lastState.Reference // 继承完整的引用
		return node
	}

	// 2. 计算哈希
	hash, err := util.CalculateSHA256(filepath.Join(workspaceRoot, relPath))
	if err != nil {
		log.Printf("警告: 无法计算哈希 %s: %v. 将其视为新文件。", relPath, err)
		node.Reference = "" // 标记为新文件
		return node
	}
	node.Hash = hash

	// 3. 哈希比对
	if originalNode, ok := idx.history.HashToNode[hash]; ok {
		// 哈希存在，说明是移动/重命名或未变更但元数据变动的文件
		// 继承其最原始的引用信息
		node.Reference = originalNode.Reference
	} else {
		// 哈希不存在，是真正的新文件，reference 留空，待打包时确定
		node.Reference = ""
	}

	return node
}
