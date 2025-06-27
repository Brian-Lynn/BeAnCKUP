package history

import (
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadHistoricalState 遍历 .beanckup 目录，加载所有历史清单，并构建一个历史状态对象。
func LoadHistoricalState(beanckupDir string) (*types.HistoricalState, error) {
	// 修复：初始化 HistoricalState 以匹配 types.go 中的新结构
	state := &types.HistoricalState{
		HashToNode:   make(map[string]*types.FileNode),
		PathToNode:   make(map[string]*types.FileNode),
		MaxSessionID: 0,
	}

	entries, err := os.ReadDir(beanckupDir)
	if err != nil {
		if os.IsNotExist(err) {
			// 目录不存在是正常情况，例如首次运行
			return state, nil
		}
		return nil, fmt.Errorf("无法读取 .beanckup 目录: %w", err)
	}

	// 按文件名（包含时间戳）排序，确保按时间顺序处理清单
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		// 只处理 manifest json 文件，忽略计划文件和配置文件
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasPrefix(entry.Name(), "Delivery_Status_") || strings.HasPrefix(entry.Name(), "config") {
			continue
		}

		manifestPath := filepath.Join(beanckupDir, entry.Name())
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			log.Printf("警告: 无法读取清单文件 %s: %v", manifestPath, err)
			continue
		}

		var manifest types.Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			log.Printf("警告: 无法解析清单文件 %s: %v", manifestPath, err)
			continue
		}

		if manifest.SessionID > state.MaxSessionID {
			state.MaxSessionID = manifest.SessionID
		}

		for _, node := range manifest.Files {
			// 更新 Path -> Node 映射，总是用最新的记录覆盖
			state.PathToNode[node.GetPath()] = node

			// 修复：更新 Hash -> Node 映射
			if node.Hash != "" {
				// 只在哈希不存在时添加。这保证了我们总是引用最早包含此内容的那个文件节点，
				// 这个节点里包含了最原始的引用信息。
				if _, exists := state.HashToNode[node.Hash]; !exists {
					state.HashToNode[node.Hash] = node
				}
			}
		}
	}

	return state, nil
}
