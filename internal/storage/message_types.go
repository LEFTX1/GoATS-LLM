package storage

import "time"

// ResumeUploadMessage 简历上传消息
type ResumeUploadMessage struct {
	// 与数据库表字段一致的主要字段
	SubmissionUUID      string    `json:"submission_uuid"`          // 提交UUID，主键
	SubmissionTimestamp time.Time `json:"submission_timestamp"`     // 提交时间戳
	SourceChannel       string    `json:"source_channel,omitempty"` // 来源渠道
	TargetJobID         string    `json:"target_job_id,omitempty"`  // 目标岗位ID
	OriginalFilename    string    `json:"original_filename"`        // 原始文件名
	OriginalFilePathOSS string    `json:"original_file_path_oss"`   // MinIO中的对象路径
	RawFileMD5          string    `json:"raw_file_md5,omitempty"`   // 原始文件的MD5，用于失败时回滚

	// 兼容性字段 (可选)
	OriginalFileObjectKey string `json:"original_file_object_key,omitempty"` // 与OriginalFilePathOSS同义
	FileURL               string `json:"file_url,omitempty"`                 // 与OriginalFilePathOSS同义
	FileName              string `json:"file_name,omitempty"`                // 与OriginalFilename同义
	UploadTime            int64  `json:"upload_time,omitempty"`              // Unix时间戳
}

// ResumeProcessingMessage 简历处理消息
type ResumeProcessingMessage struct {
	// 与数据库表字段一致的主要字段
	SubmissionUUID    string `json:"submission_uuid"`                // 提交UUID
	TargetJobID       string `json:"target_job_id,omitempty"`        // 目标岗位ID
	ParsedTextPathOSS string `json:"parsed_text_path_oss,omitempty"` // 解析文本在MinIO中的路径

	// 处理状态相关字段
	ProcessingStatus string `json:"processing_status,omitempty"` // 处理状态
	ProcessingTime   int64  `json:"processing_time,omitempty"`   // 处理时间戳

	// 文本内容 (当不想通过存储服务传递时使用)
	ParsedText string `json:"parsed_text,omitempty"` // 解析后的文本内容

	// 其他辅助字段
	ParsedTextObjectKey string   `json:"parsed_text_object_key,omitempty"` // 与ParsedTextPathOSS同义
	TextURL             string   `json:"text_url,omitempty"`               // 与ParsedTextPathOSS同义
	VectorIDs           []string `json:"vector_ids,omitempty"`             // 关联的向量ID列表
	Error               string   `json:"error,omitempty"`                  // 错误信息
}
