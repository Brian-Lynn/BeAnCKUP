package restorer

import (
	"beanckup-cli/internal/manifest"
	"beanckup-cli/internal/types"
	"encoding/json"
	"fmt"
	"io"
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
	allPackages map[string]string
}

type DeliverySession struct {
	SessionID           int
	Timestamp           time.Time
	Manifests           []*types.Manifest
	HistoricalManifests []*types.Manifest
}

func NewRestorer(deliveryDir string) (*Restorer, error) {
	return &Restorer{
		deliveryDir: deliveryDir,
		allPackages: make(map[string]string),
	}, nil
}

func (r *Restorer) DiscoverDeliverySessions() ([]*DeliverySession, error) {
	sessionMap := make(map[int]*DeliverySession)
	filepath.Walk(r.deliveryDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".7z") {
			sessionID, _, _ := parsePackageName(info.Name())
			if sessionID > 0 {
				if _, exists := sessionMap[sessionID]; !exists {
					sessionMap[sessionID] = &DeliverySession{SessionID: sessionID}
				}
				r.allPackages[info.Name()] = path
			}
		}
		return nil
	})

	var sessions []*DeliverySession
	for _, session := range sessionMap {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].SessionID < sessions[j].SessionID
	})
	return sessions, nil
}

// 修复：确保函数名与 main.go 中的调用一致
func (r *Restorer) LoadSessionManifests(session *DeliverySession, password string) error {
	var targetManifests []*types.Manifest
	var historicalManifests []*types.Manifest
	var firstTimestamp time.Time

	for packageName, packagePath := range r.allPackages {
		sessionID, _, _ := parsePackageName(packageName)
		if sessionID > 0 && sessionID <= session.SessionID {
			m, err := r.extractManifestFromPackage(packagePath, password)
			if err != nil {
				return fmt.Errorf("从包 %s 提取清单失败: %w", packageName, err)
			}
			historicalManifests = append(historicalManifests, m)
			if sessionID == session.SessionID {
				targetManifests = append(targetManifests, m)
				if firstTimestamp.IsZero() {
					if ts, err := time.Parse(time.RFC3339, m.Timestamp); err == nil {
						firstTimestamp = ts
					}
				}
			}
		}
	}

	if len(targetManifests) == 0 {
		return fmt.Errorf("未能为会话 S%d 加载任何有效的清单文件", session.SessionID)
	}

	session.Timestamp = firstTimestamp
	sort.Slice(targetManifests, func(i, j int) bool { return targetManifests[i].EpisodeID < targetManifests[j].EpisodeID })
	session.Manifests = targetManifests
	session.HistoricalManifests = historicalManifests
	return nil
}

func (r *Restorer) extractManifestFromPackage(packagePath, password string) (*types.Manifest, error) {
	tempDir, err := os.MkdirTemp("", "beanckup_manifest_*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	baseName := strings.TrimSuffix(filepath.Base(packagePath), ".7z")
	manifestFilename := baseName + ".json"
	manifestPathInPackage := filepath.ToSlash(filepath.Join(".beanckup", manifestFilename))

	args := []string{"x", packagePath, "-o" + tempDir, manifestPathInPackage, "-y"}
	if password != "" {
		args = append(args, "-p"+password)
	}

	cmd := exec.Command("7z", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("解压清单失败 (包: %s): %s", filepath.Base(packagePath), string(output))
	}

	data, err := os.ReadFile(filepath.Join(tempDir, manifestPathInPackage))
	if err != nil {
		return nil, fmt.Errorf("读取清单文件失败: %w", err)
	}
	var manifest types.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析清单失败: %w", err)
	}
	return &manifest, nil
}

func (r *Restorer) RestoreFromSession(session *DeliverySession, restorePath, password string) error {
	if len(session.Manifests) == 0 {
		return fmt.Errorf("会话 S%d 无清单文件", session.SessionID)
	}
	workspaceName := session.Manifests[0].WorkspaceName
	ts := session.Timestamp.Format("20060102_150405")
	fullRestorePath := filepath.Join(restorePath, fmt.Sprintf("%s_S%d_%s_Recovery", workspaceName, session.SessionID, ts))
	if err := os.MkdirAll(fullRestorePath, 0755); err != nil {
		return fmt.Errorf("无法创建恢复目录: %w", err)
	}

	beanckupDir := filepath.Join(fullRestorePath, ".beanckup")
	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		return fmt.Errorf("无法创建 .beanckup 目录: %w", err)
	}
	fmt.Println("正在恢复历史清单文件...")
	for _, m := range session.HistoricalManifests {
		if _, err := manifest.SaveManifest(m, beanckupDir); err != nil {
			fmt.Printf("警告: 恢复清单 '%s' 失败: %v\n", m.PackageName, err)
		}
	}

	finalFileSet := make(map[string]*types.FileNode)
	for _, m := range session.Manifests {
		for _, node := range m.Files {
			finalFileSet[node.GetPath()] = node
		}
	}
	fmt.Printf("文件将恢复到: %s\n分析完成，共需恢复 %d 个文件。\n", fullRestorePath, len(finalFileSet))

	filesBySourcePackage := make(map[string][]*types.FileNode)
	for _, node := range finalFileSet {
		if node.IsDirectory() { continue }
		parts := strings.SplitN(node.Reference, "/", 2)
		if len(parts) < 2 {
			fmt.Printf("警告: 文件 '%s' 引用格式错误: '%s'，跳过。\n", node.Path, node.Reference)
			continue
		}
		sourcePackage := parts[0]
		filesBySourcePackage[sourcePackage] = append(filesBySourcePackage[sourcePackage], node)
	}

	tempBaseDir := filepath.Join(fullRestorePath, ".beanckup_temp_restore")
	if err := os.MkdirAll(tempBaseDir, 0755); err != nil {
		return fmt.Errorf("无法创建临时恢复目录: %w", err)
	}
	defer os.RemoveAll(tempBaseDir)

	for sourcePackage, files := range filesBySourcePackage {
		sourcePackagePath, ok := r.allPackages[sourcePackage]
		if !ok {
			fmt.Printf("警告: 找不到源包 '%s'，跳过 %d 个文件。\n", sourcePackage, len(files))
			continue
		}

		fmt.Printf("\n正在从包: %s 恢复 %d 个文件...\n", sourcePackage, len(files))
		
		tempListFile, _ := os.CreateTemp(tempBaseDir, "listfile-*.txt")
		for _, node := range files {
			pathInPackage := strings.SplitN(node.Reference, "/", 2)[1]
			io.WriteString(tempListFile, filepath.ToSlash(pathInPackage)+"\n")
		}
		tempListFile.Close()

		args := []string{"x", sourcePackagePath, "-o" + tempBaseDir, "-aoa", "@" + tempListFile.Name()}
		if password != "" { args = append(args, "-p"+password) }
		
		cmd := exec.Command("7z", args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("警告: 7z 批量解压失败 (包: %s): %s\n", sourcePackage, string(output))
			os.Remove(tempListFile.Name())
			continue
		}
		os.Remove(tempListFile.Name())

		for _, node := range files {
			pathInPackage := strings.SplitN(node.Reference, "/", 2)[1]
			tempPath := filepath.Join(tempBaseDir, pathInPackage)
			finalPath := filepath.Join(fullRestorePath, node.Path)

			if _, err := os.Stat(tempPath); os.IsNotExist(err) {
				fmt.Printf("警告: 临时文件 '%s' 不存在。\n", tempPath)
				continue
			}
			if err := moveFile(tempPath, finalPath); err != nil {
				fmt.Printf("警告: 移动文件 '%s' 失败: %v\n", node.Path, err)
			}
		}
	}

	fmt.Println("\n恢复完成。")
	return nil
}

func moveFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()
	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}
	sourceFile.Close()
	return os.Remove(src)
}

func parsePackageName(packageName string) (sessionID, episodeID int, err error) {
	re := regexp.MustCompile(`S(\d+)[_-]?E(\d+)`)
	matches := re.FindStringSubmatch(packageName)
	if len(matches) != 3 {
		return 0, 0, fmt.Errorf("无法解析包名格式")
	}
	sessionID, _ = strconv.Atoi(matches[1])
	episodeID, _ = strconv.Atoi(matches[2])
	return
}
