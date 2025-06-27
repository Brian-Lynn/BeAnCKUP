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
			return state, nil
		}
		return nil, fmt.Errorf("无法读取 .beanckup 目录: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
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
			state.PathToNode[node.GetPath()] = node

			// 修复：更新 Hash -> Node 映射
			if node.Hash != "" {
				if _, exists := state.HashToNode[node.Hash]; !exists {
					state.HashToNode[node.Hash] = node
				}
			}
		}
	}

	return state, nil
}
