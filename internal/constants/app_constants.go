package constants // 定义了项目范围内的常量

import "time" // 导入时间包，用于定义时间相关的常量

const (
	// DefaultParserVer Application-level constants
	DefaultParserVer = "1.0" // 定义默认的解析器版本，可作为Tika或LLM版本的占位符

	// TenantPlaceholder Tenant placeholder - replace with actual tenant ID in usage
	TenantPlaceholder = "{tenant}" // 定义租户占位符，在实际使用中需要被替换为真实的租户ID

	// Redis Key Prefixes - incorporating tenant placeholder
	// Format: {tenant}:{namespace}:{id_or_purpose}

	// JobVectorCachePrefix JD Processing
	JobVectorCachePrefix   = TenantPlaceholder + ":job_vector:"   // 职位描述(JD)向量的缓存前缀。键格式: {tenant}:job_vector:{job_id}
	JobKeywordsCachePrefix = TenantPlaceholder + ":job_keywords:" // 职位描述(JD)关键词的缓存前缀。键格式: {tenant}:job_keywords:{job_id}

	// RawFileMD5SetKey Resume Deduplication (Original File and Parsed Text)
	RawFileMD5SetKey    = TenantPlaceholder + ":file_md5s_set" // 存储原始简历文件MD5的Redis集合键。用于文件去重。
	ParsedTextMD5SetKey = "resume_processor:md5:parsed_texts"  // 存储解析后简历文本MD5的Redis集合键。用于内容去重。

	// JobEvalCachePrefix LLM Evaluation Cache
	JobEvalCachePrefix = TenantPlaceholder + ":job_eval:" // LLM人岗匹配评估结果的缓存前缀。键格式: {tenant}:job_eval:{job_id}:{text_md5}

	// ReviewedResumesByJobTextMD5HashPrefix HR Review Tracking
	// Stores a HASH where field is hr_id and value is action string
	ReviewedResumesByJobTextMD5HashPrefix = TenantPlaceholder + ":reviewed_job_resumes:" // 追踪HR评审记录的哈希键前缀。
	// 键格式: {tenant}:reviewed_job_resumes:{job_id}:{text_md5}

	// ReviewedByHRForJobSetPrefix Stores a SET of text_md5s reviewed by a specific HR for a specific job
	ReviewedByHRForJobSetPrefix = TenantPlaceholder + ":reviewed_by_hr:" // 存储特定HR对特定职位已评审过的简历文本MD5的集合键前缀。
	// 键格式: {tenant}:reviewed_by_hr:{job_id}:{hr_id}

	// SearchSnapshotListPrefix Search Session Management
	SearchSnapshotListPrefix = TenantPlaceholder + ":search_snapshot:" // 存储搜索快照的列表键前缀，用于管理搜索会话。键格式: {tenant}:search_snapshot:{search_session_id}

	// JDCachePrefix Old constants (to be reviewed or removed if fully replaced)
	JDCachePrefix   = "jd_text:"     // 旧的职位描述文本缓存前缀（待审查或移除）
	JDCacheDuration = 24 * time.Hour // 旧的职位描述缓存时长
)

// Processing Status Constants
const (
	StatusSubmittedForProcessing  = "SUBMITTED_FOR_PROCESSING"  // 状态：已提交，等待处理
	StatusDuplicateFileSkipped    = "DUPLICATE_FILE_SKIPPED"    // 状态：因文件重复而跳过
	StatusPendingParsing          = "PENDING_PARSING"           // 状态：等待解析
	StatusContentDuplicateSkipped = "CONTENT_DUPLICATE_SKIPPED" // 状态：因内容重复而跳过
	StatusQueuedForLLM            = "QUEUED_FOR_LLM"            // 状态：已进入LLM处理队列
	StatusPendingLLM              = "PENDING_LLM"               // 状态：等待LLM处理
	StatusChunkingFailed          = "CHUNKING_FAILED"           // 状态：LLM文本分块失败
	StatusVectorizationFailed     = "VECTORIZATION_FAILED"      // 状态：向量化失败
	StatusQdrantStoreFailed       = "QDRANT_STORE_FAILED"       // 状态：向量存储到Qdrant失败
	StatusLLMProcessingFailed     = "LLM_PROCESSING_FAILED"     // 状态：LLM处理失败
	StatusMatchFailed             = "MATCH_FAILED"              // 状态：人岗匹配失败
	StatusProcessingCompleted     = "PROCESSING_COMPLETED"      // 状态：处理完成
	StatusUploadProcessingFailed  = "UPLOAD_PROCESSING_FAILED"  // 状态：上传阶段处理失败（由resume_handler引入）
)

// Log keys
const (
	// LogKeySubmissionUUID is the key for logging the submission UUID.
	LogKeySubmissionUUID = "submission_uuid"
)

// Source channels
const (
	// SourceChannelWebUpload represents submissions from web uploads.
	SourceChannelWebUpload = "web_upload"
)

// StatusSets 定义不同操作允许的状态集合
var (
	// AllowedStatusesForLLM 允许进行LLM处理的状态
	AllowedStatusesForLLM = map[string]bool{
		StatusPendingLLM:   true,
		StatusQueuedForLLM: true,
	}

	// AllowedStatusesForParsing 允许进行初始解析的状态
	AllowedStatusesForParsing = map[string]bool{
		StatusSubmittedForProcessing: true,
		StatusPendingParsing:         true,
	}

	// AllowedStatusesForRetry 允许重试的状态
	AllowedStatusesForRetry = map[string]bool{
		StatusChunkingFailed:      true,
		StatusVectorizationFailed: true,
		StatusQdrantStoreFailed:   true,
		StatusLLMProcessingFailed: true,
	}
)

// IsStatusAllowed 检查给定状态是否在允许的状态集中
func IsStatusAllowed(status string, allowedSet map[string]bool) bool {
	return allowedSet[status]
}

// 这是一个格式化Redis键的辅助函数说明。
// 理想情况下，这个函数应该位于Redis工具包或storage.Redis模块中。
// 在这里定义只是为了阐明这些常量将如何被使用。
// func FormatKey(tenantID, prefix string, parts ...string) string {
// 	 base := strings.Replace(prefix, TenantPlaceholder, tenantID, 1)
// 	 return base + strings.Join(parts, ":")
// }
