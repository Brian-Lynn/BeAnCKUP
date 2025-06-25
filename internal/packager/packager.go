package packager

import (
	"beanckup-cli/internal/types"
	"bufio"
	"encoding/json"
	"fmt"
	"io"
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

// CreatePackage 负责创建包含指定文件和特定清单的7z包
func (p *Packager) CreatePackage(
	deliveryPath string,
	// 这是随包清单，只包含本包相关文件信息
	packageManifest *types.Manifest,
	workspaceRoot string,
	// 这是实际要打包的新文件列表
	newFiles []*types.FileNode,
	password string,
	compressionLevel int,
	progressCallback func(Progress),
) error {
	if err := os.MkdirAll(deliveryPath, 0755); err != nil {
		return fmt.Errorf("无法创建交付目录: %w", err)
	}

	packageFilePath := filepath.Join(deliveryPath, packageManifest.PackageName)

	// 创建临时打包目录
	tempPackDir, err := os.MkdirTemp("", "beanckup_pack_*")
	if err != nil {
		return fmt.Errorf("无法创建临时打包目录: %w", err)
	}
	defer os.RemoveAll(tempPackDir)

	// 创建 .beanckup 子目录用于存放manifest
	beanckupSubDir := filepath.Join(tempPackDir, ".beanckup")
	if err := os.MkdirAll(beanckupSubDir, 0755); err != nil {
		return fmt.Errorf("无法创建临时 .beanckup 目录: %w", err)
	}

	// 将manifest保存到临时目录的 .beanckup 子目录中
	manifestData, err := json.MarshalIndent(packageManifest, "", "  ")
	if err != nil {
		return fmt.Errorf("无法序列化随包清单: %w", err)
	}

	// 使用标准的manifest文件名格式
	baseName := packageManifest.PackageName[:len(packageManifest.PackageName)-3] // 移除 .7z
	manifestFilename := baseName + ".json"
	manifestPath := filepath.Join(beanckupSubDir, manifestFilename)
	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		return fmt.Errorf("无法写入临时清单文件: %w", err)
	}

	// 复制所有新文件到临时目录，保持其相对路径结构
	for _, node := range newFiles {
		sourcePath := filepath.Join(workspaceRoot, node.Path)
		destPath := filepath.Join(tempPackDir, node.Path)
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return fmt.Errorf("无法在临时目录中创建子目录: %w", err)
		}
		if err := copyFile(sourcePath, destPath); err != nil {
			return fmt.Errorf("无法复制文件 '%s' 到临时目录: %w", node.Path, err)
		}
	}

	// 构建7z命令参数，参考旧版的进度输出参数
	args := []string{
		"a",                                     // 添加文件
		fmt.Sprintf("-mx=%d", compressionLevel), // 压缩级别
		"-mmt=on",                               // 多线程
		"-mhe=on",                               // 加密目录
		"-bb3",                                  // 输出最大详细信息
		"-bsp1",                                 // 输出标准进度信息
		"-bso1",                                 // 将所有标准输出重定向到stdout
		packageFilePath,                         // 输出文件
	}

	if password != "" {
		args = append(args, "-p"+password)
	}

	// 切换到临时目录，然后打包所有内容
	args = append(args, ".")

	cmd := exec.Command("7z", args...)
	cmd.Dir = tempPackDir // 设置工作目录为临时目录

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("无法获取 stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 7z 命令失败: %w", err)
	}

	// 实时解析7z输出
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		progress := parse7zProgress(line, packageManifest.PackageName)
		progressCallback(progress)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("7z 命令执行失败: %w", err)
	}

	// 发送完成信号
	progressCallback(Progress{Percentage: 100, PackageName: packageManifest.PackageName, Stage: "完成"})
	return nil
}

func copyFile(src, dst string) error {
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

	// 获取源文件的时间戳
	sourceInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// 设置目标文件的时间戳为源文件的时间戳
	return os.Chtimes(dst, sourceInfo.ModTime(), sourceInfo.ModTime())
}

// parse7zProgress 解析7z的详细输出，参考旧版的方法
func parse7zProgress(line, packageName string) Progress {
	progress := Progress{
		PackageName: packageName,
		Stage:       "准备中",
	}

	// 移除ANSI颜色代码
	line = removeANSICodes(line)

	// 解析百分比进度
	if percentage := extractPercentage(line); percentage >= 0 {
		progress.Percentage = percentage
	}

	// 解析当前处理的文件
	if currentFile := extractCurrentFile(line); currentFile != "" {
		progress.CurrentFile = currentFile
	}

	// 解析处理阶段
	if stage := extractStage(line); stage != "" {
		progress.Stage = stage
	}

	return progress
}

// removeANSICodes 移除ANSI转义序列
func removeANSICodes(s string) string {
	ansiRegex := regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)
	return ansiRegex.ReplaceAllString(s, "")
}

// extractPercentage 提取百分比数值
func extractPercentage(line string) int {
	// 匹配各种百分比格式：45%, 45 %, 45% 等
	percentageRegex := regexp.MustCompile(`(\d+)\s*%`)
	matches := percentageRegex.FindStringSubmatch(line)
	if len(matches) > 1 {
		if p, err := strconv.Atoi(matches[1]); err == nil {
			return p
		}
	}
	return -1
}

// extractCurrentFile 提取当前处理的文件名
func extractCurrentFile(line string) string {
	// 7z输出中通常包含文件路径，查找常见的文件路径模式
	// 简化处理，只显示文件名，不显示完整路径
	fileRegex := regexp.MustCompile(`[^\\/]+\.\w+$|[^\\/]+$`)
	matches := fileRegex.FindAllString(line, -1)
	if len(matches) > 0 {
		// 返回最后一个匹配的文件名
		return matches[len(matches)-1]
	}
	return ""
}

// extractStage 提取处理阶段
func extractStage(line string) string {
	line = strings.ToLower(line)

	if strings.Contains(line, "analyzing") || strings.Contains(line, "分析") {
		return "分析文件"
	} else if strings.Contains(line, "compressing") || strings.Contains(line, "压缩") {
		return "压缩中"
	} else if strings.Contains(line, "writing") || strings.Contains(line, "写入") {
		return "写入文件"
	} else if strings.Contains(line, "creating") || strings.Contains(line, "创建") {
		return "创建包"
	} else if strings.Contains(line, "updating") || strings.Contains(line, "更新") {
		return "更新包"
	}

	return ""
}
