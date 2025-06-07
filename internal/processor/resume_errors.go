package processor // 定义了简历处理相关的核心逻辑和组件

import (
	"errors" // 导入标准错误包
	"fmt"    // 导入格式化I/O包
)

// 定义基础错误类型
var (
	ErrResumeDownloadFailed = errors.New("下载简历失败")         // 定义简历下载失败的基础错误
	ErrParseTextFailed      = errors.New("提取简历文本失败")       // 定义提取简历文本失败的基础错误
	ErrStoreTextFailed      = errors.New("上传解析文本失败")       // 定义上传解析后文本失败的基础错误
	ErrPublishMessageFailed = errors.New("发布消息到LLM解析队列失败") // 定义发布消息失败的基础错误
	ErrUpdateStatusFailed   = errors.New("更新简历状态失败")       // 定义更新简历状态失败的基础错误
	ErrDatabaseFailed       = errors.New("数据库操作失败")        // 定义数据库操作失败的基础错误
)

// ResumeProcessError 包含详细错误信息的自定义错误结构体
type ResumeProcessError struct {
	SubmissionUUID string // 相关的简历提交UUID
	Op             string // 发生错误的操作名称 (e.g., "download", "parse")
	BaseErr        error  // 包装的基础错误类型 (e.g., ErrResumeDownloadFailed)
	Detail         string // 具体的错误细节信息
}

// Error 实现 error 接口，返回格式化的错误字符串
func (e *ResumeProcessError) Error() string {
	if e.Detail != "" { // 如果有详细信息
		return fmt.Sprintf("%s (操作:%s, UUID:%s): %s", e.BaseErr, e.Op, e.SubmissionUUID, e.Detail) // 返回包含细节的错误信息
	}
	return fmt.Sprintf("%s (操作:%s, UUID:%s)", e.BaseErr, e.Op, e.SubmissionUUID) // 返回不含细节的错误信息
}

// Unwrap 实现 Go 1.13+ 的错误包装接口，允许 errors.Is 和 errors.As 检查被包装的错误
func (e *ResumeProcessError) Unwrap() error {
	return e.BaseErr // 返回被包装的基础错误
}

// Is 实现 errors.Is 接口以支持错误比较
func (e *ResumeProcessError) Is(target error) bool {
	return errors.Is(e.BaseErr, target) // 判断基础错误是否与目标错误相同
}

// NewDownloadError 创建一个新的下载错误
func NewDownloadError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,                    // 设置UUID
		Op:             "download",              // 设置操作为 "download"
		BaseErr:        ErrResumeDownloadFailed, // 设置基础错误
		Detail:         detail,                  // 设置详细信息
	}
}

// NewParseError 创建一个新的解析错误
func NewParseError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,               // 设置UUID
		Op:             "parse",            // 设置操作为 "parse"
		BaseErr:        ErrParseTextFailed, // 设置基础错误
		Detail:         detail,             // 设置详细信息
	}
}

// NewStoreError 创建一个新的存储错误
func NewStoreError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,               // 设置UUID
		Op:             "store",            // 设置操作为 "store"
		BaseErr:        ErrStoreTextFailed, // 设置基础错误
		Detail:         detail,             // 设置详细信息
	}
}

// NewPublishError 创建一个新的发布消息错误
func NewPublishError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,                    // 设置UUID
		Op:             "publish",               // 设置操作为 "publish"
		BaseErr:        ErrPublishMessageFailed, // 设置基础错误
		Detail:         detail,                  // 设置详细信息
	}
}

// NewUpdateError 创建一个新的更新状态错误
func NewUpdateError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,                  // 设置UUID
		Op:             "update",              // 设置操作为 "update"
		BaseErr:        ErrUpdateStatusFailed, // 设置基础错误
		Detail:         detail,                // 设置详细信息
	}
}

// NewDatabaseError 创建一个新的数据库操作错误
func NewDatabaseError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,              // 设置UUID
		Op:             "database",        // 设置操作为 "database"
		BaseErr:        ErrDatabaseFailed, // 设置基础错误
		Detail:         detail,            // 设置详细信息
	}
}
