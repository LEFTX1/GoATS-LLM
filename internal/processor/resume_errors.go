package processor

import (
	"errors"
	"fmt"
)

// 定义基础错误类型
var (
	ErrResumeDownloadFailed = errors.New("下载简历失败")
	ErrParseTextFailed      = errors.New("提取简历文本失败")
	ErrStoreTextFailed      = errors.New("上传解析文本失败")
	ErrPublishMessageFailed = errors.New("发布消息到LLM解析队列失败")
	ErrUpdateStatusFailed   = errors.New("更新简历状态失败")
	ErrDatabaseFailed       = errors.New("数据库操作失败")
)

// ResumeProcessError 包含详细错误信息的自定义错误
type ResumeProcessError struct {
	SubmissionUUID string
	Op             string
	BaseErr        error
	Detail         string
}

func (e *ResumeProcessError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s (操作:%s, UUID:%s): %s", e.BaseErr, e.Op, e.SubmissionUUID, e.Detail)
	}
	return fmt.Sprintf("%s (操作:%s, UUID:%s)", e.BaseErr, e.Op, e.SubmissionUUID)
}

func (e *ResumeProcessError) Unwrap() error {
	return e.BaseErr
}

// Is 实现 errors.Is 接口以支持错误比较
func (e *ResumeProcessError) Is(target error) bool {
	return errors.Is(e.BaseErr, target)
}

// 错误构造函数
func NewDownloadError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,
		Op:             "download",
		BaseErr:        ErrResumeDownloadFailed,
		Detail:         detail,
	}
}

func NewParseError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,
		Op:             "parse",
		BaseErr:        ErrParseTextFailed,
		Detail:         detail,
	}
}

func NewStoreError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,
		Op:             "store",
		BaseErr:        ErrStoreTextFailed,
		Detail:         detail,
	}
}

func NewPublishError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,
		Op:             "publish",
		BaseErr:        ErrPublishMessageFailed,
		Detail:         detail,
	}
}

func NewUpdateError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,
		Op:             "update",
		BaseErr:        ErrUpdateStatusFailed,
		Detail:         detail,
	}
}

func NewDatabaseError(uuid, detail string) error {
	return &ResumeProcessError{
		SubmissionUUID: uuid,
		Op:             "database",
		BaseErr:        ErrDatabaseFailed,
		Detail:         detail,
	}
}
