package packager

import (
	"beanckup-cli/internal/types"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Progress struct {
	Percentage  int
	PackageName string
	CurrentFile string
	Stage       string
}

type Packager struct{}

func NewPackager() *Packager {
	return &Packager{}
}

// CreatePackage 使用“直接提货单”模式创建7z包，不再需要临时“集散地”
func (p *Packager) CreatePackage(
	deliveryPath string,
	packageManifest *types.Manifest,
	workspaceRoot string,
	newFiles []*types.FileNode,
	password string,
	compressionLevel int,
	progressCallback func(Progress),
) error {
	// 确保最终输出目录存在，但我们不会在这里创建任何集散地。
	if err := os.MkdirAll(deliveryPath, 0755); err != nil {
		return fmt.Errorf("无法创建交付目录: %w", err)
	}

	// 1. 在操作系a统的默认临时目录 (通常是本地SSD) 创建一个极小的元数据文件夹。
	//    这完全符合您的要求：临时文件在本地，不写入输出盘。
	tempMetaDir, err := os.MkdirTemp("", "beanckup_meta_*")
	if err != nil {
		return fmt.Errorf("无法创建本地临时元数据目录: %w", err)
	}
	defer os.RemoveAll(tempMetaDir)

	// 2. 将动态生成的 manifest.json 写入这个本地临时目录
	beanckupSubDir := filepath.Join(tempMetaDir, ".beanckup")
	if err := os.MkdirAll(beanckupSubDir, 0755); err != nil {
		return fmt.Errorf("无法创建本地临时 .beanckup 目录: %w", err)
	}
	manifestData, err := json.MarshalIndent(packageManifest, "", "  ")
	if err != nil {
		return fmt.Errorf("无法序列化随包清单: %w", err)
	}
	manifestPath := filepath.Join(beanckupSubDir, strings.TrimSuffix(packageManifest.PackageName, ".7z")+".json")
	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		return fmt.Errorf("无法写入本地临时清单文件: %w", err)
	}

	// 3. 创建本地“提货单”(listfile)，列出所有需要打包的文件的绝对路径
	listFilePath := filepath.Join(tempMetaDir, "listfile.txt")
	listFile, err := os.Create(listFilePath)
	if err != nil {
		return fmt.Errorf("无法创建本地提货单文件: %w", err)
	}
	for _, node := range newFiles {
		// 写入源文件的绝对路径，让7z直接去硬盘A取货
		absPath := filepath.Join(workspaceRoot, node.Path)
		listFile.WriteString(absPath + "\n")
	}
	listFile.Close() // 必须关闭才能让7z读取

	// --- 4. 调用7z进行打包 ---
	packageFilePath := filepath.Join(deliveryPath, packageManifest.PackageName)
	
	// a. 先根据“提货单”打包源文件到硬盘B
	// 使用 -w 开关，让7z去掉源文件的盘符和根路径，只保留相对路径
	args := []string{
		"a",
		packageFilePath,
		"@" + listFilePath, // 让7z读取提货单
		fmt.Sprintf("-w%s", workspaceRoot), // **关键**: 设置工作目录为源盘的工作区，7z会自动处理为相对路径
		fmt.Sprintf("-mx=%d", compressionLevel),
		"-mmt=on",                               // 多线程
		"-mhe=on",                               // 加密目录
		"-bb3",                                  // 输出最大详细信息
		"-bsp1",                                 // 输出标准进度信息
		"-bso1", 
	}
	if password != "" {
		args = append(args, "-p"+password)
	}
	
	cmd := exec.Command("7z", args...)
	
	// 执行打包并处理进度
	if err := run7zAndHandleProgress(cmd, packageManifest.PackageName, progressCallback); err != nil {
		return fmt.Errorf("打包源文件失败: %w", err)
	}

	// b. 使用 "u" (update) 命令，将本地临时目录中的 manifest.json 添加到硬盘B的压缩包中
	updateArgs := []string{
		"u",
		packageFilePath,
		filepath.Join(tempMetaDir, ".beanckup"), // 添加整个 .beanckup 文件夹
		fmt.Sprintf("-w%s", tempMetaDir),      // **关键**: 设置工作目录为本地临时元数据目录
	}
	if password != "" {
		updateArgs = append(updateArgs, "-p"+password)
	}
	updateCmd := exec.Command("7z", updateArgs...)
	if err := updateCmd.Run(); err != nil {
		return fmt.Errorf("添加清单到压缩包失败: %w", err)
	}
	
	progressCallback(Progress{Percentage: 100, PackageName: packageManifest.PackageName, Stage: "完成"})
	return nil
}

// run7zAndHandleProgress 是一个辅助函数，用于执行7z命令并实时处理进度输出
func run7zAndHandleProgress(cmd *exec.Cmd, packageName string, progressCallback func(Progress)) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法获取 stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("无法获取 stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 7z 命令失败: %w", err)
	}
	
	go func() { 
		// 在后台静默处理stderr，避免阻塞
		io.Copy(io.Discard, stderr) 
	}()

	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\r')
		if len(line) > 0 {
			progress := parse7zProgress(line, packageName)
			progressCallback(progress)
		}
		if err != nil {
			if err != io.EOF {
				log.Printf("读取7z输出时出错: %v", err)
			}
			break
		}
	}

	return cmd.Wait()
}


func parse7zProgress(line, packageName string) Progress {
	progress := Progress{
		PackageName: packageName,
		Stage:       "压缩中",
	}
	
	parts := strings.Split(strings.TrimSpace(line), " ")
	for _, part := range parts {
		if strings.HasSuffix(part, "%") {
			pStr := strings.TrimSuffix(part, "%")
			if p, err := strconv.Atoi(pStr); err == nil {
				progress.Percentage = p
				break
			}
		}
	}
	
	re := regexp.MustCompile(`\s(?:U|A)\s(.+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		progress.CurrentFile = matches[1]
	}

	return progress
}
