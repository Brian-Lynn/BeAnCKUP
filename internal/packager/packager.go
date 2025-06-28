package packager

import (
	"beanckup-cli/internal/types"
	"bufio"
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

// Progress 结构体定义了打包过程中的进度信息
type Progress struct {
	Percentage  int
	PackageName string
	CurrentFile string
	Stage       string
}

// Packager 结构体封装了打包相关的功能
type Packager struct{}

// NewPackager 创建一个新的 Packager 实例
func NewPackager() *Packager {
	return &Packager{}
}

// CreatePackage 使用最简单、最可靠的“一次性打包”模型。
// 它接收一个包含所有数据文件和清单文件的列表，然后执行一次 `7z a` 命令。
func (p *Packager) CreatePackage(
	deliveryPath string,
	packageName string, // 只需要包名用于显示
	workspaceRoot string,
	filesToPack []*types.FileNode, // 包含数据文件和清单文件
	password string,
	compressionLevel int,
	packageSizeLimitMB int,
	progressCallback func(Progress),
) error {
	// 1. 在系统临时目录创建 listfile.txt
	tempListDir, err := os.MkdirTemp("", "beanckup_list_*")
	if err != nil {
		return fmt.Errorf("无法创建临时列表目录: %w", err)
	}
	defer os.RemoveAll(tempListDir)

	listFilePath := filepath.Join(tempListDir, "listfile.txt")
	listFile, err := os.Create(listFilePath)
	if err != nil {
		return fmt.Errorf("无法创建文件列表: %w", err)
	}
	for _, node := range filesToPack {
		// 写入所有文件的相对路径
		listFile.WriteString(node.Path + "\n")
	}
	listFile.Close()

	// 2. 准备7z命令参数
	packageFilePath := filepath.Join(deliveryPath, packageName)
	args := []string{
		"a",
		packageFilePath,    // 最终输出的压缩包基础名
		"@" + listFilePath, // 让7z根据列表读取文件
		"-w" + deliveryPath, // 强制临时文件在交付目录生成
		fmt.Sprintf("-mx=%d", compressionLevel),
		"-mmt=on",
		"-bb3",
		"-bsp1",
		"-bso1",
	}

	// 3. 判断是否需要分卷
	var episodeTotalSize int64
	for _, node := range filesToPack {
		// 注意：清单文件本身很小，其大小对是否分卷的判断影响可忽略
		episodeTotalSize += node.Size
	}
	packageSizeLimitBytes := int64(packageSizeLimitMB) * 1024 * 1024
	if packageSizeLimitMB > 0 && episodeTotalSize > packageSizeLimitBytes {
		args = append(args, fmt.Sprintf("-v%dm", packageSizeLimitMB))
	}

	if password != "" {
		args = append(args, "-p"+password, "-mhe=on")
	}

	// 4. 执行一次性的 `7z a` 命令
	cmd := exec.Command("7z", args...)
	cmd.Dir = workspaceRoot // 将工作目录设置为源工作区，以便7z能通过相对路径找到所有文件

	if err := run7zAndHandleProgress(cmd, packageName, "打包文件和清单", progressCallback); err != nil {
		// 如果打包失败，尝试删除可能产生的未完成的包
		os.Remove(packageFilePath)
		// 同时尝试清理可能产生的分卷文件
		globPattern := packageFilePath + ".0*"
		if files, _ := filepath.Glob(globPattern); files != nil {
			for _, f := range files {
				os.Remove(f)
			}
		}
		return fmt.Errorf("创建压缩包失败: %w", err)
	}

	progressCallback(Progress{Percentage: 100, PackageName: packageName, Stage: "完成"})
	return nil
}

// run7zAndHandleProgress 保持不变，它能很好地处理进度
func run7zAndHandleProgress(cmd *exec.Cmd, packageName, stage string, progressCallback func(Progress)) error {
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

	go io.Copy(io.Discard, stderr)

	reader := bufio.NewReader(stdout)
	for {
		line, err := reader.ReadString('\r')
		if len(line) > 0 {
			progress := parse7zProgress(line, packageName, stage)
			if progress.Percentage > 0 || strings.Contains(line, "%") {
				progressCallback(progress)
			}
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

// parse7zProgress 保持不变
func parse7zProgress(line, packageName, stage string) Progress {
	progress := Progress{
		PackageName: packageName,
		Stage:       stage,
	}

	rePercent := regexp.MustCompile(`(\d+)\%`)
	matchesPercent := rePercent.FindStringSubmatch(line)
	if len(matchesPercent) > 1 {
		if p, err := strconv.Atoi(matchesPercent[1]); err == nil {
			progress.Percentage = p
		}
	}

	reFile := regexp.MustCompile(`\s(U|A|Compressing)\s+(.+)`)
	matchesFile := reFile.FindStringSubmatch(line)
	if len(matchesFile) > 2 {
		progress.CurrentFile = strings.TrimSpace(matchesFile[2])
	}

	return progress
}
