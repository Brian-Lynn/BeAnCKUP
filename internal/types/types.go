package types

import "time"

// --- 配置相关 ---

// Config 保存了用户的所有设置
type Config struct {
	WorkspacePath      string `json:"workspace_path"`
	DeliveryPath       string `json:"delivery_path"`
	RestorePath        string `json:"restore_path"`
	PackageSizeLimitMB int    `json:"package_size_limit_mb"`
	TotalSizeLimitMB   int    `json:"total_size_limit_mb"` // 0 表示无限制
	CompressionLevel   int    `json:"compression_level"`
	Password           string `json:"password"`
}

// --- 文件与扫描相关 ---

// FileNode 代表一个文件或目录在某个时间点的状态。
type FileNode struct {
	Path       string    `json:"path,omitempty"`       // 文件在工作区的相对路径 (e.g., "data/image.jpg")
	Dir        string    `json:"dir,omitempty"`        // 目录路径，与path互斥
	Size       int64     `json:"size,omitempty"`       // 文件大小
	ModTime    time.Time `json:"mod_time,omitempty"`   // 修改时间
	CreateTime time.Time `json:"create_time,omitempty"`// 创建时间
	Hash       string    `json:"hash,omitempty"`       // 文件内容的 SHA256 哈希
	// Reference 指向该文件物理实体所在的位置。
	// 格式: "packagename.7z/path/inside/package.jpg"
	// 对于新文件，打包时会指向自己所在的包。
	// 对于引用文件，会指向历史包和其在历史包中的路径。
	Reference string `json:"reference,omitempty"`
}

// IsDirectory 检查是否为目录
func (n *FileNode) IsDirectory() bool {
	return n.Dir != ""
}

// GetPath 获取文件或目录路径
func (n *FileNode) GetPath() string {
	if n.IsDirectory() {
		return n.Dir
	}
	return n.Path
}

// FilterNewFiles 是一个辅助函数，用于从节点列表中筛选出所有新文件
// 新文件的定义是其 Reference 为空，因为它的引用位置尚未确定。
func FilterNewFiles(nodes []*FileNode) []*FileNode {
	var newFiles []*FileNode
	for _, node := range nodes {
		if !node.IsDirectory() && node.Reference == "" {
			newFiles = append(newFiles, node)
		}
	}
	return newFiles
}

// FilterReferenceFiles 是一个辅助函数，用于从节点列表中筛选出所有引用文件
// 引用文件的定义是其 Reference 字段不为空。
func FilterReferenceFiles(nodes []*FileNode) []*FileNode {
	var referenceFiles []*FileNode
	for _, node := range nodes {
		if !node.IsDirectory() && node.Reference != "" {
			referenceFiles = append(referenceFiles, node)
		}
	}
	return referenceFiles
}

// HistoricalState 持有从所有过去的 manifest 文件中加载的信息
type HistoricalState struct {
	// Hash -> 原始文件节点 的映射，用于查找移动/重命名文件的原始路径
	HashToNode map[string]*FileNode
	// Path -> 原始文件节点 的映射，用于检测文件是否变更
	PathToNode   map[string]*FileNode
	MaxSessionID int
}

// --- 交付计划与会话相关 ---

type EpisodeStatus string

const (
	EpisodeStatusPending       EpisodeStatus = "PENDING"
	EpisodeStatusInProgress    EpisodeStatus = "IN_PROGRESS"
	EpisodeStatusCompleted     EpisodeStatus = "COMPLETED"
	EpisodeStatusExceededLimit EpisodeStatus = "EXCEEDED_LIMIT" // 超出总大小限制
)

// Episode 代表一个具体的交付包计划
type Episode struct {
	ID        int           `json:"id"`
	TotalSize int64         `json:"total_size"`
	Files     []*FileNode   `json:"files"`
	Status    EpisodeStatus `json:"status"`
}

// Plan 代表一次完整的交付会话计划
type Plan struct {
	SessionID      int         `json:"session_id"`
	Timestamp      time.Time   `json:"timestamp"`
	TotalNewSize   int64       `json:"total_new_size"`
	Episodes       []Episode   `json:"episodes"`
	AllNodes       []*FileNode `json:"-"`
	StatusFilePath string      `json:"-"`
}

// IsCompleted 检查整个交付计划是否已完成
func (p *Plan) IsCompleted() bool {
	if len(p.Episodes) == 0 {
		return true // 没有包的计划视为已完成
	}
	for _, ep := range p.Episodes {
		if ep.Status != EpisodeStatusCompleted {
			return false
		}
	}
	return true
}

// CountPending 统计还有多少个待办的交付包
func (p *Plan) CountPending() int {
	count := 0
	for _, ep := range p.Episodes {
		if ep.Status == EpisodeStatusPending || ep.Status == EpisodeStatusInProgress {
			count++
		}
	}
	return count
}

// --- 清单相关 ---

// Manifest 代表单个交付包内容的精确描述
type Manifest struct {
	WorkspaceName string      `json:"workspace_name"`
	SessionID     int         `json:"session_id"`
	EpisodeID     int         `json:"episode_id"`
	Timestamp     string      `json:"timestamp"`
	PackageName   string      `json:"package_name"`
	Files         []*FileNode `json:"files"`
}
