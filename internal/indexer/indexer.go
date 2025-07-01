package indexer

import (
	"beanckup-cli/internal/types"
	"beanckup-cli/internal/util"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// Indexer 负责扫描工作区并根据历史记录对文件进行分类。
type Indexer struct {
	history *types.HistoricalState
}

// Job 包含一个要处理的文件路径及其文件信息
type Job struct {
	Path string
	Info os.FileInfo
}

// Result 包含处理后的文件节点和任何可能发生的错误
type Result struct {
	Node *types.FileNode
	Err  error
}

// NewIndexer 创建一个新的 Indexer 实例。
func NewIndexer(history *types.HistoricalState) *Indexer {
	return &Indexer{history: history}
}

// ScanWithProgress 使用生产者-消费者模型并行扫描文件，以提高I/O和CPU效率。
func (idx *Indexer) ScanWithProgress(workspacePath string, progressCallback func(string)) ([]*types.FileNode, error) {
	var allNodes []*types.FileNode
	var filesToScan []string

	// 【核心修正】: 根目录扫描时，定义系统排除项
	isRootScan := util.IsRoot(workspacePath)
	systemExclusions := map[string]bool{
		"$recycle.bin":              true,
		"system volume information": true,
		"pagefile.sys":              true,
		"swapfile.sys":              true,
		"hiberfil.sys":              true,
		"dumpstack.log.tmp":         true,
	}

	// 1. 生产者准备：预扫描以获取文件总数，用于进度条
	filepath.Walk(workspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// 【核心修正】: 应用排除规则
		if isRootScan && path != workspacePath {
			if parent, err := filepath.Rel(workspacePath, filepath.Dir(path)); err == nil && parent == "." {
				if systemExclusions[strings.ToLower(info.Name())] {
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}

		if !info.IsDir() {
			if strings.Contains(path, ".beanckup") || strings.EqualFold(info.Name(), "Thumbs.db") {
				return nil
			}
			filesToScan = append(filesToScan, path)
		} else if info.IsDir() && info.Name() == ".beanckup" {
			return filepath.SkipDir
		}
		return nil
	})

	totalFiles := len(filesToScan)
	if totalFiles == 0 {
		// 如果没有文件需要扫描，直接返回空列表
		return allNodes, nil
	}

	var processedFiles int64 // 使用原子操作保证多协程计数的线程安全

	// 2. 创建通道和等待组
	jobs := make(chan Job, totalFiles)
	results := make(chan Result, totalFiles)
	var wg sync.WaitGroup

	// 3. 启动消费者（Workers），数量根据CPU核心数决定
	numWorkers := runtime.NumCPU()
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				relPath, err := filepath.Rel(workspacePath, job.Path)
				if err != nil {
					results <- Result{Err: fmt.Errorf("无法获取相对路径: %w", err)}
					continue
				}
				relPath = filepath.ToSlash(relPath)

				node := idx.classifyFile(workspacePath, relPath, job.Info)
				results <- Result{Node: node}

				// 在worker中安全地更新进度
				currentProgress := atomic.AddInt64(&processedFiles, 1)
				progress := fmt.Sprintf("扫描进度: %d/%d 文件 (%.1f%%) - %s",
					currentProgress, totalFiles, float64(currentProgress)/float64(totalFiles)*100, relPath)
				progressCallback(progress)
			}
		}()
	}

	// 4. 生产者：遍历文件系统，将任务放入 jobs 通道
	err := filepath.Walk(workspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				log.Printf("[警告] 权限不足，跳过: %s", path)
				return nil // 权限错误，跳过该文件或目录
			}
			return err // 其他错误，中断扫描
		}

		// 【核心修正】: 应用排除规则
		if isRootScan && path != workspacePath {
			if parent, err := filepath.Rel(workspacePath, filepath.Dir(path)); err == nil && parent == "." {
				if systemExclusions[strings.ToLower(info.Name())] {
					log.Printf("[信息] 根据根目录排除规则，跳过系统项: %s", path)
					if info.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
			}
		}

		if info.IsDir() && info.Name() == ".beanckup" {
			return filepath.SkipDir
		}

		if path == workspacePath {
			return nil
		}

		if info.IsDir() {
			// 目录节点直接在主协程处理，因为它们不涉及耗时操作
			relPath, _ := filepath.Rel(workspacePath, path)
			relPath = filepath.ToSlash(relPath)
			allNodes = append(allNodes, &types.FileNode{Dir: relPath, ModTime: info.ModTime().UTC()})
		} else {
			// 文件任务放入通道，交由worker处理
			if !strings.EqualFold(info.Name(), "Thumbs.db") {
				jobs <- Job{Path: path, Info: info}
			}
		}
		return nil
	})

	close(jobs) // 所有任务已发送完毕，关闭通道

	if err != nil {
		// 如果遍历文件出错，要确保能正常退出
		wg.Wait()
		close(results)
		return nil, err
	}

	// 5. 收集结果：启动一个协程等待所有worker完成，然后关闭结果通道
	go func() {
		wg.Wait()
		close(results)
	}()

	// 在主协程中安全地收集所有结果
	for result := range results {
		if result.Err != nil {
			log.Printf("扫描中发生错误: %v", result.Err)
			continue
		}
		if result.Node != nil {
			allNodes = append(allNodes, result.Node)
		}
	}

	return allNodes, nil
}

// classifyFile 函数的逻辑保持不变，它现在被 worker 并发调用
func (idx *Indexer) classifyFile(workspaceRoot, relPath string, info os.FileInfo) *types.FileNode {
	fullPath := filepath.Join(workspaceRoot, relPath)

	node := &types.FileNode{Path: relPath, Size: info.Size(), ModTime: info.ModTime().UTC()}
	cTime, err := util.GetCreationTime(fullPath)
	if err == nil {
		node.CreateTime = cTime.UTC()
	}

	// 五元预筛
	if lastState, ok := idx.history.PathToNode[relPath]; ok &&
		!lastState.IsDirectory() && lastState.Size == node.Size &&
		lastState.ModTime.Equal(node.ModTime) && lastState.CreateTime.Equal(node.CreateTime) {
		node.Hash = lastState.Hash
		node.Reference = lastState.Reference
		return node
	}

	// 计算哈希
	hash, err := util.CalculateSHA256(fullPath)
	if err != nil {
		log.Printf("警告: 无法计算哈希 %s: %v. 将其视为新文件。", relPath, err)
		node.Reference = ""
		return node
	}
	node.Hash = hash

	// 哈希比对
	if originalNode, ok := idx.history.HashToNode[hash]; ok {
		node.Reference = originalNode.Reference
	} else {
		node.Reference = ""
	}

	return node
}
