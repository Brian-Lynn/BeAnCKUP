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

// Scan 递归扫描工作区路径，并返回一个包含所有已分类文件节点的扁平列表。
func (idx *Indexer) Scan(workspacePath string) ([]*types.FileNode, error) {
	var allNodes []*types.FileNode

	absWorkspacePath, err := filepath.Abs(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("无法获取绝对路径: %w", err)
	}

	err = filepath.Walk(absWorkspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".beanckup" {
			return filepath.SkipDir
		}
		if path == absWorkspacePath {
			return nil
		}

		relPath, err := filepath.Rel(absWorkspacePath, path)
		if err != nil {
			return fmt.Errorf("无法获取相对路径: %w", err)
		}
		relPath = filepath.ToSlash(relPath)

		node := idx.classifyFile(absWorkspacePath, relPath, info)
		allNodes = append(allNodes, node)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return allNodes, nil
}

// classifyFile 对单个文件或目录进行分类。
func (idx *Indexer) classifyFile(workspaceRoot, relPath string, info os.FileInfo) *types.FileNode {
	fullPath := filepath.Join(workspaceRoot, relPath)

	if info.IsDir() {
		// 目录节点
		node := &types.FileNode{
			Dir:     relPath,
			ModTime: info.ModTime().UTC(),
		}

		cTime, err := util.GetCreationTime(fullPath)
		if err == nil {
			node.CreateTime = cTime.UTC()
		}

		return node
	}

	// 文件节点 - 获取五要素元数据
	node := &types.FileNode{
		Path:    relPath,
		Size:    info.Size(),
		ModTime: info.ModTime().UTC(),
	}

	cTime, err := util.GetCreationTime(fullPath)
	if err == nil {
		node.CreateTime = cTime.UTC()
	}

	// 快速比对：检查五要素是否完全匹配
	lastState, exists := idx.history.PathToNode[relPath]
	if exists && fiveElementsMatch(lastState, node) {
		// 五要素完全匹配，说明文件未变更，标记为引用
		node.Classification = types.CLASSIFIED_REFERENCE
		node.Hash = lastState.Hash

		// 从哈希映射中查找正确的引用路径
		if lastState.Hash != "" {
			if refPath, hashExists := idx.history.HashToRefPackage[lastState.Hash]; hashExists {
				node.Reference = refPath
			}
		}

		return node
	}

	// 五要素不匹配，需要计算哈希进行进一步检查
	hash, err := util.CalculateSHA256(fullPath)
	if err != nil {
		log.Printf("警告: 无法计算哈希 %s: %v. 将其视为新文件.", fullPath, err)
		node.Classification = types.CLASSIFIED_NEW
		return node
	}

	// 移除sha256-前缀
	if strings.HasPrefix(hash, "sha256-") {
		hash = hash[7:]
	}
	node.Hash = hash

	// 哈希比对：检查是否存在于历史哈希集合中
	if refPath, hashExists := idx.history.HashToRefPackage[hash]; hashExists {
		// 哈希存在，说明文件内容未变，只是元数据变了（如路径、文件名）
		node.Classification = types.CLASSIFIED_REFERENCE
		// 直接使用历史记录中的引用路径
		node.Reference = refPath
	} else {
		// 哈希是全新的，说明是真正的新增或修改文件
		node.Classification = types.CLASSIFIED_NEW
	}

	return node
}

// fiveElementsMatch 检查五要素是否完全匹配
func fiveElementsMatch(oldNode, newNode *types.FileNode) bool {
	return oldNode.Size == newNode.Size &&
		oldNode.ModTime.Equal(newNode.ModTime) &&
		oldNode.CreateTime.Equal(newNode.CreateTime)
}
