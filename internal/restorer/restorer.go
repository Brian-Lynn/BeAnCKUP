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

	// 递归搜索交付包目录
	err := filepath.Walk(r.deliveryDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// 只处理.7z文件
		if !info.IsDir() && strings.HasSuffix(info.Name(), ".7z") {
			packagePath := path
			packageName := info.Name()

			fmt.Printf("发现包文件: %s\n", packageName)

			// 尝试从包名解析会话信息
			sessionID, episodeID, err := parsePackageName(packageName)
			if err != nil {
				fmt.Printf("跳过无法解析的包: %s (错误: %v)\n", packageName, err)
				return nil // 跳过无法解析的包
			}

			fmt.Printf("解析成功: %s -> S%02dE%02d\n", packageName, sessionID, episodeID)

			// 记录包信息
			r.packages[packageName] = packagePath

			// 创建或更新会话
			session, exists := sessionMap[sessionID]
			if !exists {
				session = &DeliverySession{
					SessionID:    sessionID,
					Timestamp:    time.Now(), // 临时时间，稍后从清单更新
					Manifests:    []*types.Manifest{},
					PackagePath:  filepath.Dir(packagePath),
					AllManifests: make(map[string]*types.Manifest),
				}
				sessionMap[sessionID] = session
				fmt.Printf("创建新会话: S%02d\n", sessionID)
			}
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("搜索交付包失败: %w", err)
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

// RestoreFromSession 从指定会话恢复文件
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
			// 只处理已加载到的包
			if _, loaded := session.AllManifests[packageName]; !loaded {
				continue
			}
			// 生成manifest文件名
			baseName := packageName[:len(packageName)-3] // 去掉.7z
			manifestFilename := baseName + ".json"
			manifestPathInPackage := ".beanckup/" + manifestFilename
			args := []string{
				"e",
				packagePath,
				manifestPathInPackage,
				"-o" + beanckupDir,
				"-y",
			}
			cmd := exec.Command("7z", args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				fmt.Printf("警告: 解压manifest清单 '%s' 失败: %v\nOutput: %s\n", manifestPathInPackage, err, string(output))
			}
		}
	}

	// 收集所有需要恢复的文件
	allFiles := make(map[string]*types.FileNode)
	// 从所有清单中收集文件
	for _, manifest := range session.Manifests {
		for _, node := range manifest.Files {
			key := node.GetPath()
			if !node.IsDirectory() {
				allFiles[key] = node
			}
		}
	}
	fmt.Printf("发现 %d 个文件需要恢复\n", len(allFiles))

	// 恢复每个文件
	for path, node := range allFiles {
		if node.IsDirectory() {
			dirPath := filepath.Join(fullRestorePath, node.Dir)
			os.MkdirAll(dirPath, 0755)
			os.Chtimes(dirPath, node.ModTime, node.CreateTime)
			continue
		}
		sourcePackageName, sourceFilePathInPackage, err := r.findSourceFileInSession(session, node, password)
		if err != nil {
			fmt.Printf("警告: 无法定位源文件 '%s': %v\n", path, err)
			continue
		}
		sourcePackagePath := r.packages[sourcePackageName]
		if sourcePackagePath == "" {
			fmt.Printf("警告: 无法找到包文件 '%s'\n", sourcePackageName)
			continue
		}
		fmt.Printf("正在解压: %s (来自 %s)\n", path, sourcePackageName)
		targetDir := filepath.Join(fullRestorePath, filepath.Dir(path))
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			fmt.Printf("警告: 无法创建目录 '%s': %v\n", targetDir, err)
			continue
		}
		// 解压文件到正确的位置，使用7z e，不保留包内目录结构
		args := []string{
			"e", // 只解压文件，不保留目录结构
			sourcePackagePath,
			"-o" + targetDir, // 直接解压到目标目录
			sourceFilePathInPackage,
			"-y", // 全部选是
		}
		if password != "" {
			args = append(args, "-p"+password)
		}

		cmd := exec.Command("7z", args...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			fmt.Printf("警告: 解压文件 '%s' 失败: %v\nOutput: %s\n", sourceFilePathInPackage, err, string(output))
			continue
		}

		// 检查文件是否在目标目录
		fileName := filepath.Base(sourceFilePathInPackage)
		actualPath := filepath.Join(targetDir, fileName)
		if _, err := os.Stat(actualPath); err != nil {
			fmt.Printf("警告: 解压后未找到文件: %s\n", actualPath)
			continue
		}

		// 如果目标路径和实际路径不一致，移动到manifest指定的最终位置
		expectedPath := filepath.Join(fullRestorePath, path)
		if actualPath != expectedPath {
			if err := os.Rename(actualPath, expectedPath); err != nil {
				fmt.Printf("警告: 无法移动文件从 '%s' 到 '%s': %v\n", actualPath, expectedPath, err)
				continue
			}
		}
	}
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
