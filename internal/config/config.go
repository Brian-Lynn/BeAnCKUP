package config

import (
	"beanckup-cli/internal/types"
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const configFileName = "config.json"
const lastWorkspaceFile = ".last_workspace"

// GetWorkspacePath 获取工作区路径，优先从记录文件读取，否则让用户输入
func GetWorkspacePath(reader *bufio.Reader) string {
	// 尝试从程序旁边读取上次记录
	exePath, err := os.Executable()
	if err == nil {
		lastWsPathFile := filepath.Join(filepath.Dir(exePath), lastWorkspaceFile)
		if data, err := os.ReadFile(lastWsPathFile); err == nil {
			lastPath := strings.TrimSpace(string(data))
			if _, err := os.Stat(lastPath); err == nil {
				if askForConfirmation(fmt.Sprintf("是否继续使用上次的工作区 '%s'?", lastPath), reader) {
					return lastPath
				}
			}
		}
	}

	// 如果没有记录或用户选择否，则要求输入
	newPath := askForPath(reader, "请输入或拖入您的工作区文件夹:")

	// 保存本次选择
	if exePath != "" {
		lastWsPathFile := filepath.Join(filepath.Dir(exePath), lastWorkspaceFile)
		os.WriteFile(lastWsPathFile, []byte(newPath), 0644)
	}

	return newPath
}

// LoadConfig 从工作区加载配置文件
func LoadConfig(workspacePath string) (*types.Config, error) {
	configPath := filepath.Join(workspacePath, ".beanckup", configFileName)

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var config types.Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return &config, nil
}

// SaveConfig 保存配置到工作区
func SaveConfig(workspacePath string, config *types.Config) error {
	beanckupDir := filepath.Join(workspacePath, ".beanckup")
	if err := os.MkdirAll(beanckupDir, 0755); err != nil {
		return fmt.Errorf("无法创建 .beanckup 目录: %w", err)
	}

	configPath := filepath.Join(beanckupDir, configFileName)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("写入配置文件失败: %w", err)
	}

	return nil
}

// SetupWizard 通过交互式向导设置配置
func SetupWizard(reader *bufio.Reader) *types.Config {
	config := &types.Config{}

	fmt.Println("\n=== BeanCKUP 配置向导 ===")

	// 交付路径
	fmt.Print("请输入交付包保存路径: ")
	input, _ := reader.ReadString('\n')
	config.DeliveryPath = strings.TrimSpace(input)
	if config.DeliveryPath == "" {
		config.DeliveryPath = "./delivery"
	}

	// 恢复路径
	fmt.Print("请输入文件恢复路径: ")
	input, _ = reader.ReadString('\n')
	config.RestorePath = strings.TrimSpace(input)
	if config.RestorePath == "" {
		config.RestorePath = "./restore"
	}

	// 包大小限制
	fmt.Print("请输入单个包大小限制 (MB, 0表示无限制): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size >= 0 {
		config.PackageSizeLimitMB = size
	} else {
		config.PackageSizeLimitMB = 100
	}

	// 总大小限制
	fmt.Print("请输入总大小限制 (MB, 0表示无限制): ")
	input, _ = reader.ReadString('\n')
	if size, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && size >= 0 {
		config.TotalSizeLimitMB = size
	} else {
		config.TotalSizeLimitMB = 0
	}

	// 压缩级别
	fmt.Print("请输入压缩级别 (0-9, 0=不压缩, 9=最大压缩): ")
	input, _ = reader.ReadString('\n')
	if level, err := strconv.Atoi(strings.TrimSpace(input)); err == nil && level >= 0 && level <= 9 {
		config.CompressionLevel = level
	} else {
		config.CompressionLevel = 5
	}

	// 密码
	fmt.Print("请输入加密密码 (留空表示不加密): ")
	input, _ = reader.ReadString('\n')
	config.Password = strings.TrimSpace(input)

	return config
}

func askForPath(reader *bufio.Reader, prompt string) string {
	for {
		fmt.Println(prompt)
		path, _ := reader.ReadString('\n')
		path = strings.TrimSpace(path)
		// 移除windows路径可能带有的引号
		path = strings.Trim(path, "\"")
		if _, err := os.Stat(path); err == nil {
			return path
		}
		fmt.Printf("错误: 路径 '%s' 不存在或无法访问，请重新输入。\n", path)
	}
}

func askForInt(reader *bufio.Reader, prompt string, defaultValue int) int {
	for {
		fmt.Printf("%s (默认: %d): ", prompt, defaultValue)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)
		if input == "" {
			return defaultValue
		}
		if val, err := strconv.Atoi(input); err == nil {
			return val
		}
		fmt.Println("错误: 无效输入，请输入一个数字。")
	}
}

func askForConfirmation(prompt string, reader *bufio.Reader) bool {
	fmt.Printf("%s (y/n): ", prompt)
	input, _ := reader.ReadString('\n')
	return strings.ToLower(strings.TrimSpace(input)) == "y"
}

// PrintConfig 打印当前配置
func PrintConfig(config *types.Config) {
	fmt.Printf("工作区路径: %s\n", config.WorkspacePath)
	fmt.Printf("交付路径: %s\n", config.DeliveryPath)
	fmt.Printf("恢复路径: %s\n", config.RestorePath)
	fmt.Printf("包大小限制: %d MB\n", config.PackageSizeLimitMB)
	if config.TotalSizeLimitMB > 0 {
		fmt.Printf("总大小限制: %d MB\n", config.TotalSizeLimitMB)
	} else {
		fmt.Printf("总大小限制: 无限制\n")
	}
	fmt.Printf("压缩级别: %d\n", config.CompressionLevel)
	if config.Password != "" {
		fmt.Printf("加密: 已启用\n")
	} else {
		fmt.Printf("加密: 未启用\n")
	}
}
