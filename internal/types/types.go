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

type FileClassification string

const (
	CLASSIFIED_NEW       FileClassification = "NEW"
	CLASSIFIED_REFERENCE FileClassification = "REFERENCE"
)

type ReferenceInfo struct {
	ReferencePackage string `json:"reference_package,omitempty"`
}

type FileNode struct {
	Path           string             `json:"path,omitempty"`
	Dir            string             `json:"dir,omitempty"` // 目录路径，与path互斥
	Size           int64              `json:"size,omitempty"`
	ModTime        time.Time          `json:"mod_time,omitempty"`
	CreateTime     time.Time          `json:"create_time,omitempty"`
	Hash           string             `json:"hash,omitempty"`
	Classification FileClassification `json:"classification,omitempty"`
	Reference      string             `json:"reference,omitempty"` // 直接存储引用路径
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
func FilterNewFiles(nodes []*FileNode) []*FileNode {
	var newFiles []*FileNode
	for _, node := range nodes {
		// 只包含真正的新文件，不包括未变更的文件
		if node.Classification == CLASSIFIED_NEW && !node.IsDirectory() {
			newFiles = append(newFiles, node)
		}
	}
	return newFiles
}

// FilterReferenceFiles 是一个辅助函数，用于从节点列表中筛选出所有引用文件
func FilterReferenceFiles(nodes []*FileNode) []*FileNode {
	var referenceFiles []*FileNode
	for _, node := range nodes {
		// 只包含引用文件，不包括目录
		if node.Classification == CLASSIFIED_REFERENCE && !node.IsDirectory() {
			referenceFiles = append(referenceFiles, node)
		}
	}
	return referenceFiles
}

// HistoricalState 持有从所有过去的 manifest 文件中加载的信息
type HistoricalState struct {
	HashToRefPackage map[string]string
	PathToNode       map[string]*FileNode
	MaxSessionID     int
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
	SessionID    int         `json:"session_id"`
	Timestamp    time.Time   `json:"timestamp"`
	TotalNewSize int64       `json:"total_new_size"` // 本次计划交付的总文件大小
	Episodes     []Episode   `json:"episodes"`
	AllNodes     []*FileNode `json:"-"` // 不保存到json，仅运行时使用
}

// IsCompleted 检查整个交付计划是否已完成
func (p *Plan) IsCompleted() bool {
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

// CountExceededLimit 统计有多少个超出总大小限制的包
func (p *Plan) CountExceededLimit() int {
	count := 0
	for _, ep := range p.Episodes {
		if ep.Status == EpisodeStatusExceededLimit {
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
