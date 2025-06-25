package history

import (
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings" // <-- 修复: 添加了缺失的导入
)

// LoadHistoricalState 遍历 .beanckup 目录，加载所有历史清单，并构建一个历史状态对象。
func LoadHistoricalState(beanckupDir string) (*types.HistoricalState, error) {
	state := &types.HistoricalState{
		HashToRefPackage: make(map[string]string),
		PathToNode:       make(map[string]*types.FileNode),
		MaxSessionID:     0,
	}

	entries, err := os.ReadDir(beanckupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil // 目录不存在，返回空状态
		}
		return nil, fmt.Errorf("无法读取 .beanckup 目录: %w", err)
	}

	// 按文件名排序，确保按时间顺序处理清单
	sort.Slice(entries, func(i, j int) bool {
		// 按文件名排序，这样会按时间戳顺序处理
		return entries[i].Name() < entries[j].Name()
	})

	fmt.Printf("正在加载历史状态，发现 %d 个文件\n", len(entries))

	// 显示排序后的文件列表
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			fmt.Printf("  发现清单文件: %s\n", entry.Name())
		}
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
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

		fmt.Printf("加载清单: %s (会话 %d, 包 %d, %d 个文件)\n",
			entry.Name(), manifest.SessionID, manifest.EpisodeID, len(manifest.Files))

		if manifest.SessionID > state.MaxSessionID {
			state.MaxSessionID = manifest.SessionID
		}

		for _, node := range manifest.Files {
			state.PathToNode[node.GetPath()] = node
			if node.Hash != "" {
				// 只有被物理存储的 NEW 文件才加入哈希->包的映射
				if node.Classification == types.CLASSIFIED_NEW {
					// 存储格式：包名/原始路径
					refPath := manifest.PackageName + "/" + node.GetPath()
					if _, exists := state.HashToRefPackage[node.Hash]; !exists {
						state.HashToRefPackage[node.Hash] = refPath
						fmt.Printf("  添加哈希映射: %s -> %s\n", node.Hash[:8]+"...", refPath)
					}
				} else if node.Classification == types.CLASSIFIED_REFERENCE && node.Reference != "" {
					// 对于引用文件，如果已经有引用路径，验证其正确性
					// 如果没有引用路径，说明这是第一次遇到这个文件，应该指向当前包
					if node.Reference == "" {
						refPath := manifest.PackageName + "/" + node.GetPath()
						if _, exists := state.HashToRefPackage[node.Hash]; !exists {
							state.HashToRefPackage[node.Hash] = refPath
							fmt.Printf("  添加引用文件哈希映射: %s -> %s\n", node.Hash[:8]+"...", refPath)
						}
					}
				}
			}
		}
	}

	fmt.Printf("历史状态加载完成: 最大会话ID=%d, 路径映射=%d, 哈希映射=%d\n",
		state.MaxSessionID, len(state.PathToNode), len(state.HashToRefPackage))

	return state, nil
}
