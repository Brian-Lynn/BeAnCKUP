package restorer

import (
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Restorer struct {
	deliveryDir string
	packages    map[string]string // 包名 -> 包文件路径
}

type DeliverySession struct {
	SessionID    int
	Timestamp    time.Time
	Manifests    []*types.Manifest
	PackagePath  string
	AllManifests map[string]*types.Manifest
}

type PackageInfo struct {
	PackageName string
	PackagePath string
	SessionID   int
	EpisodeID   int
	Timestamp   string
}

// FileToRestore 包含恢复单个文件所需的所有信息
type FileToRestore struct {
	Node              *types.FileNode // 来自原始 manifest 的文件节点
	SourcePackageName string          // 文件来源的包名
	SourceFilePath    string          // 文件在源包中的完整路径
	FinalPath         string          // 文件恢复后的最终绝对路径（包括重命名）
}

func NewRestorer(deliveryDir string) (*Restorer, error) {
	return &Restorer{
		deliveryDir: deliveryDir,
		packages:    make(map[string]string),
	}, nil
}

// DiscoverDeliverySessions 发现交付包目录中的所有会话
func (r *Restorer) DiscoverDeliverySessions() ([]*DeliverySession, error) {
	var sessions []*DeliverySession
	sessionMap := make(map[int]*DeliverySession)

	fmt.Printf("开始搜索交付包目录: %s\n", r.deliveryDir)

	// 只遍历一层目录和文件
	entries, err := os.ReadDir(r.deliveryDir)
	if err != nil {
		return nil, fmt.Errorf("无法读取目录: %w", err)
	}

	for _, entry := range entries {
		path := filepath.Join(r.deliveryDir, entry.Name())
		if entry.IsDir() {
			// 只遍历一层子目录
			subEntries, err := os.ReadDir(path)
			if err != nil {
				if os.IsPermission(err) {
					fmt.Printf("跳过无权限目录或文件: %s\n", path)
					continue
				}
				return nil, err
			}
			for _, subEntry := range subEntries {
				subPath := filepath.Join(path, subEntry.Name())
				if !subEntry.IsDir() && strings.HasSuffix(subEntry.Name(), ".7z") {
					packageName := subEntry.Name()
					fmt.Printf("发现包文件: %s\n", packageName)
					// 尝试从包名解析会话信息
					sessionID, episodeID, err := parsePackageName(packageName)
					if err != nil {
						fmt.Printf("跳过无法解析的包: %s (错误: %v)\n", packageName, err)
						continue
					}
					fmt.Printf("解析成功: %s -> S%02dE%02d\n", packageName, sessionID, episodeID)
					r.packages[packageName] = subPath
					session, exists := sessionMap[sessionID]
					if !exists {
						session = &DeliverySession{
							SessionID:    sessionID,
							Timestamp:    time.Now(),
							Manifests:    []*types.Manifest{},
							PackagePath:  filepath.Dir(subPath),
							AllManifests: make(map[string]*types.Manifest),
						}
						sessionMap[sessionID] = session
						fmt.Printf("创建新会话: S%02d\n", sessionID)
					}
				}
			}
		} else if strings.HasSuffix(entry.Name(), ".7z") {
			packageName := entry.Name()
			fmt.Printf("发现包文件: %s\n", packageName)
			// 尝试从包名解析会话信息
			sessionID, episodeID, err := parsePackageName(packageName)
			if err != nil {
				fmt.Printf("跳过无法解析的包: %s (错误: %v)\n", packageName, err)
				continue
			}
			fmt.Printf("解析成功: %s -> S%02dE%02d\n", packageName, sessionID, episodeID)
			r.packages[packageName] = path
			session, exists := sessionMap[sessionID]
			if !exists {
				session = &DeliverySession{
					SessionID:    sessionID,
					Timestamp:    time.Now(),
					Manifests:    []*types.Manifest{},
					PackagePath:  filepath.Dir(path),
					AllManifests: make(map[string]*types.Manifest),
				}
				sessionMap[sessionID] = session
				fmt.Printf("创建新会话: S%02d\n", sessionID)
			}
		}
	}

	fmt.Printf("发现 %d 个会话\n", len(sessionMap))

	// 转换为切片并排序
	for _, session := range sessionMap {
		// 按EpisodeID排序清单
		sort.Slice(session.Manifests, func(i, j int) bool {
			return session.Manifests[i].EpisodeID < session.Manifests[j].EpisodeID
		})
		sessions = append(sessions, session)
	}

	// 按SessionID排序
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SessionID < sessions[j].SessionID
	})

	return sessions, nil
}

// LoadSessionManifests 加载指定会话的所有清单文件
func (r *Restorer) LoadSessionManifests(session *DeliverySession, password string) error {
	// 找到该会话及之前的所有包（智能加载）
	var sessionPackages []PackageInfo
	for packageName, packagePath := range r.packages {
		sessionID, episodeID, err := parsePackageName(packageName)
		if err != nil {
			continue
		}
		// 只加载当前会话及之前的包（S1-S4，不能往后找）
		if sessionID <= session.SessionID {
			sessionPackages = append(sessionPackages, PackageInfo{
				PackageName: packageName,
				PackagePath: packagePath,
				SessionID:   sessionID,
				EpisodeID:   episodeID,
			})
		}
	}

	// 按SessionID和EpisodeID排序
	sort.Slice(sessionPackages, func(i, j int) bool {
		if sessionPackages[i].SessionID != sessionPackages[j].SessionID {
			return sessionPackages[i].SessionID < sessionPackages[j].SessionID
		}
		return sessionPackages[i].EpisodeID < sessionPackages[j].EpisodeID
	})

	fmt.Printf("智能加载: 发现 %d 个相关包 (S01-S%02d)\n", len(sessionPackages), session.SessionID)

	// 为每个包提取清单文件
	allManifests := make(map[string]*types.Manifest) // 包名 -> 清单
	for _, pkg := range sessionPackages {
		manifest, err := r.extractManifestFromPackage(pkg.PackagePath, password)
		if err != nil {
			return fmt.Errorf("从包 %s 提取清单失败: %w", pkg.PackageName, err)
		}
		allManifests[pkg.PackageName] = manifest
		fmt.Printf("  加载包: %s\n", pkg.PackageName)
	}

	// 只使用目标会话的清单作为主清单，但保留所有包的清单用于文件查找
	session.Manifests = []*types.Manifest{}
	for _, pkg := range sessionPackages {
		if pkg.SessionID == session.SessionID {
			if manifest, exists := allManifests[pkg.PackageName]; exists {
				session.Manifests = append(session.Manifests, manifest)
			}
		}
	}

	// 按EpisodeID排序主清单
	sort.Slice(session.Manifests, func(i, j int) bool {
		return session.Manifests[i].EpisodeID < session.Manifests[j].EpisodeID
	})

	// 更新会话时间戳为第一个包的实际时间
	if len(session.Manifests) > 0 {
		if timestamp, err := time.Parse(time.RFC3339, session.Manifests[0].Timestamp); err == nil {
			session.Timestamp = timestamp
		}
	}

	// 保存所有清单用于文件查找
	session.AllManifests = allManifests

	return nil
}

// extractManifestFromPackage 从7z包中提取清单文件
func (r *Restorer) extractManifestFromPackage(packagePath, password string) (*types.Manifest, error) {
	// 创建临时目录
	tempDir, err := os.MkdirTemp("", "beanckup_manifest_*")
	if err != nil {
		return nil, fmt.Errorf("创建临时目录失败: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// 从包名生成标准的manifest文件名
	packageName := filepath.Base(packagePath)
	baseName := packageName[:len(packageName)-3] // 移除 .7z
	manifestFilename := baseName + ".json"
	manifestPathInPackage := ".beanckup/" + manifestFilename

	// 构建7z命令参数
	args := []string{
		"x", // 提取
		packagePath,
		"-o" + tempDir,
		manifestPathInPackage, // 提取标准的清单文件
		"-y",                  // 全部选是
	}
	if password != "" {
		args = append(args, "-p"+password)
	}

	// 执行7z命令
	cmd := exec.Command("7z", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		// 如果失败，可能是密码错误
		if strings.Contains(string(output), "password") || strings.Contains(string(output), "Wrong password") {
			return nil, fmt.Errorf("密码错误或包需要密码")
		}
		return nil, fmt.Errorf("解压清单文件失败: %w, output: %s", err, string(output))
	}

	// 读取清单文件
	manifestPath := filepath.Join(tempDir, ".beanckup", manifestFilename)
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

// parsePackageName 解析包名格式: workspace-S01E01-20250625094947.7z
func parsePackageName(packageName string) (sessionID, episodeID int, err error) {
	re := regexp.MustCompile(`S(\d+)E(\d+)`)
	matches := re.FindStringSubmatch(packageName)
	if len(matches) != 3 {
		return 0, 0, fmt.Errorf("无法解析包名格式")
	}
	sessionID, err = strconv.Atoi(matches[1])
	if err != nil {
		return 0, 0, err
	}
	episodeID, err = strconv.Atoi(matches[2])
	if err != nil {
		return 0, 0, err
	}
	return sessionID, episodeID, nil
}

// RestoreFromSession 从指定会话恢复文件（采用7z命令行批处理模式）
func (r *Restorer) RestoreFromSession(session *DeliverySession, restorePath, password string) error {
	if len(session.Manifests) == 0 {
		return fmt.Errorf("会话无清单文件")
	}
	manifest := session.Manifests[0]
	workspaceName := manifest.WorkspaceName
	sessionID := session.SessionID
	ts := session.Timestamp.Format("20060102_150405")
	recoveryDir := fmt.Sprintf("%s_S%02d_%s_Recovery", workspaceName, sessionID, ts)
	fullRestorePath := filepath.Join(restorePath, recoveryDir)
	fmt.Printf("恢复到目录: %s\n", fullRestorePath)

	if err := os.MkdirAll(fullRestorePath, 0755); err != nil {
		return fmt.Errorf("无法创建恢复目录 %s: %w", fullRestorePath, err)
	}

	// === 先恢复所有相关包的manifest清单到恢复区的 .beanckup 目录 ===
	beanckupDir := filepath.Join(fullRestorePath, ".beanckup")
	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		fmt.Printf("警告: 无法创建 .beanckup 目录: %v\n", err)
	} else {
		for packageName, packagePath := range r.packages {
			if _, loaded := session.AllManifests[packageName]; !loaded {
				continue
			}
			baseName := strings.TrimSuffix(packageName, ".7z")
			manifestFilename := baseName + ".json"
			manifestPathInPackage := ".beanckup/" + manifestFilename
			args := []string{"e", packagePath, "-o" + beanckupDir, manifestPathInPackage, "-y"}
			if password != "" {
				args = append(args, "-p"+password)
			}
			cmd := exec.Command("7z", args...)
			if output, err := cmd.CombinedOutput(); err != nil {
				fmt.Printf("警告: 解压 manifest '%s' 失败: %v\nOutput: %s\n", manifestPathInPackage, err, string(output))
			}
		}
	}

	// 1. 收集所有需要恢复的文件，并按源包分组
	filesToRestoreByPackage := make(map[string][]*FileToRestore)
	allFiles := make(map[string]*types.FileNode)
	for _, manifest := range session.Manifests {
		for _, node := range manifest.Files {
			if !node.IsDirectory() {
				allFiles[node.GetPath()] = node
			}
		}
	}
	fmt.Printf("发现 %d 个文件需要恢复\n", len(allFiles))
	for path, node := range allFiles {
		sourcePackageName, sourceFilePathInPackage, err := r.findSourceFileInSession(session, node, password)
		if err != nil {
			fmt.Printf("警告: 无法定位源文件 '%s': %v\n", path, err)
			continue
		}
		if _, ok := filesToRestoreByPackage[sourcePackageName]; !ok {
			filesToRestoreByPackage[sourcePackageName] = []*FileToRestore{}
		}
		filesToRestoreByPackage[sourcePackageName] = append(filesToRestoreByPackage[sourcePackageName], &FileToRestore{
			Node:              node,
			SourcePackageName: sourcePackageName,
			SourceFilePath:    sourceFilePathInPackage,
			FinalPath:         filepath.Join(fullRestorePath, path),
		})
	}

	// 2. 遍历每个包，执行批量解压和重命名
	for packageName, filesToRestore := range filesToRestoreByPackage {
		sourcePackagePath, ok := r.packages[packageName]
		if !ok {
			fmt.Printf("警告: 无法找到包文件 '%s' 的路径，跳过\n", packageName)
			continue
		}

		fmt.Printf("\n正在处理包: %s (%d 个文件)\n", packageName, len(filesToRestore))

		// 创建临时解压目录
		tempExtractDir := filepath.Join(os.TempDir(), fmt.Sprintf("beanckup_restore_%d", hashString(packageName)))
		if err := os.MkdirAll(tempExtractDir, 0755); err != nil {
			fmt.Printf("警告: 创建临时目录 '%s' 失败: %v\n", tempExtractDir, err)
			continue
		}
		defer os.RemoveAll(tempExtractDir)

		// 创建临时文件列表
		tempListFile := filepath.Join(tempExtractDir, "filelist.txt")
		file, err := os.Create(tempListFile)
		if err != nil {
			fmt.Printf("警告: 创建临时文件列表 '%s' 失败: %v\n", tempListFile, err)
			continue
		}
		for _, ftr := range filesToRestore {
			_, _ = file.WriteString(ftr.SourceFilePath + "\n")
		}
		file.Close()

		// 执行7z批量解压
		fmt.Printf("  -> 批量解压到临时目录: %s\n", tempExtractDir)
		args := []string{"x", sourcePackagePath, "-o" + tempExtractDir, "-aoa", "@" + tempListFile}
		if password != "" {
			args = append(args, "-p"+password)
		}
		cmd := exec.Command("7z", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("警告: 7z 批量解压失败 for package '%s': %v\nOutput: %s\n", packageName, err, string(output))
			continue
		}

		// 移动并重命名文件
		fmt.Println("  -> 移动并重命名文件到最终位置...")
		for _, ftr := range filesToRestore {
			tempPath := filepath.Join(tempExtractDir, ftr.SourceFilePath)
			finalPath := ftr.FinalPath

			if _, err := os.Stat(tempPath); os.IsNotExist(err) {
				fmt.Printf("警告: 临时解压文件 '%s' 不存在，跳过\n", tempPath)

				continue
			}

			// 确保最终目标目录存在
			finalDir := filepath.Dir(finalPath)
			if err := os.MkdirAll(finalDir, 0755); err != nil {
				fmt.Printf("警告: 无法创建最终目录 '%s': %v\n", finalDir, err)
				continue
			}

			// 执行移动和重命名
			if err := os.Rename(tempPath, finalPath); err != nil {
				fmt.Printf("警告: 无法移动/重命名文件从 '%s' 到 '%s': %v\n", tempPath, finalPath, err)

				continue
			}
		}
	}

	fmt.Println("\n恢复完成。")
	return nil
}

// findSourceFileInSession 在会话中查找源文件
func (r *Restorer) findSourceFileInSession(session *DeliverySession, node *types.FileNode, password string) (string, string, error) {
	if node.Classification == types.CLASSIFIED_NEW {
		// 新文件，在当前会话的包中查找
		for _, manifest := range session.Manifests {
			for _, manifestNode := range manifest.Files {
				if manifestNode.GetPath() == node.GetPath() && manifestNode.Classification == types.CLASSIFIED_NEW {
					return manifest.PackageName, node.GetPath(), nil
				}
			}
		}
		return "", "", fmt.Errorf("在会话中未找到新文件 '%s'", node.GetPath())
	}

	if node.Classification == types.CLASSIFIED_REFERENCE {
		// 对于引用文件，严格按照引用路径查找
		if node.Reference != "" {
			parts := strings.SplitN(node.Reference, "/", 2)
			if len(parts) == 2 {
				refPackage := parts[0]
				refPath := parts[1]

				// 在引用的包中查找文件，忽略时间戳差异
				for packageName, refManifest := range session.AllManifests {
					// 检查包名是否匹配（忽略时间戳）
					if isPackageNameMatch(refPackage, packageName) {
						for _, historicalNode := range refManifest.Files {
							if historicalNode.GetPath() == refPath {
								fmt.Printf("  按引用找到: %s -> %s\n", node.GetPath(), packageName)
								return packageName, refPath, nil
							}
						}
					}
				}
			}
		}

		// 如果引用路径找不到，尝试在所有相关包中查找该文件（智能回退）
		for packageName, manifest := range session.AllManifests {
			for _, historicalNode := range manifest.Files {
				if historicalNode.GetPath() == node.GetPath() {
					fmt.Printf("  智能回退找到: %s -> %s\n", node.GetPath(), packageName)
					return packageName, node.GetPath(), nil
				}
			}
		}

		return "", "", fmt.Errorf("在所有相关包中未找到文件 '%s' (引用: %s)", node.GetPath(), node.Reference)
	}

	return "", "", fmt.Errorf("不支持的文件分类 '%s'", node.Classification)
}

// isPackageNameMatch 检查包名是否匹配（忽略时间戳）
func isPackageNameMatch(refPackage, packageName string) bool {
	// 提取SessionID和EpisodeID进行比较
	re := regexp.MustCompile(`S(\d+)E(\d+)`)

	refMatches := re.FindStringSubmatch(refPackage)
	if len(refMatches) != 3 {
		return false
	}
	refSessionID, err := strconv.Atoi(refMatches[1])
	if err != nil {
		return false
	}
	refEpisodeID, err := strconv.Atoi(refMatches[2])
	if err != nil {
		return false
	}

	pkgMatches := re.FindStringSubmatch(packageName)
	if len(pkgMatches) != 3 {
		return false
	}
	pkgSessionID, err := strconv.Atoi(pkgMatches[1])
	if err != nil {
		return false
	}
	pkgEpisodeID, err := strconv.Atoi(pkgMatches[2])
	if err != nil {
		return false
	}

	return refSessionID == pkgSessionID && refEpisodeID == pkgEpisodeID
}

// hashString 用于为临时目录生成唯一的名称
func hashString(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h = h ^ uint32(s[i])
		h = h * 16777619
	}
	return h
}
