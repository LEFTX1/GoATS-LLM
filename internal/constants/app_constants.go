package constants

import "time"

const (
	// Application-level constants
	DefaultParserVer = "1.0" // Placeholder for actual Tika/LLM versions

	// Tenant placeholder - replace with actual tenant ID in usage
	TenantPlaceholder = "{tenant}" // Or load from config/context

	// Redis Key Prefixes - incorporating tenant placeholder
	// Format: {tenant}:{namespace}:{id_or_purpose}

	// JD Processing
	JobVectorCachePrefix   = TenantPlaceholder + ":job_vector:"   // Stores serialized JD vector. Key: {tenant}:job_vector:{job_id}
	JobKeywordsCachePrefix = TenantPlaceholder + ":job_keywords:" // Stores JSON string of JD keywords. Key: {tenant}:job_keywords:{job_id}

	// Resume Deduplication (Original File and Parsed Text)
	RawFileMD5SetKey    = TenantPlaceholder + ":file_md5s_set" // Set of raw file MD5s. Key: {tenant}:file_md5s_set
	ParsedTextMD5SetKey = TenantPlaceholder + ":text_md5s_set" // Set of parsed text MD5s. Key: {tenant}:text_md5s_set

	// LLM Evaluation Cache
	JobEvalCachePrefix = TenantPlaceholder + ":job_eval:" // Stores serialized JobMatchEvaluation. Key: {tenant}:job_eval:{job_id}:{text_md5}

	// HR Review Tracking
	// Stores a HASH where field is hr_id and value is action string
	ReviewedResumesByJobTextMD5HashPrefix = TenantPlaceholder + ":reviewed_job_resumes:"
	// Key: {tenant}:reviewed_job_resumes:{job_id}:{text_md5}

	// Stores a SET of text_md5s reviewed by a specific HR for a specific job
	ReviewedByHRForJobSetPrefix = TenantPlaceholder + ":reviewed_by_hr:"
	// Key: {tenant}:reviewed_by_hr:{job_id}:{hr_id}

	// Search Session Management
	SearchSnapshotListPrefix = TenantPlaceholder + ":search_snapshot:" // Stores a LIST of sorted text_md5s. Key: {tenant}:search_snapshot:{search_session_id}

	// Old constants (to be reviewed or removed if fully replaced)
	JDCachePrefix   = "jd_text:" // Example: "default_tenant:jd_text:"
	JDCacheDuration = 24 * time.Hour
)

// Processing Status Constants
const (
	StatusSubmittedForProcessing  = "SUBMITTED_FOR_PROCESSING"
	StatusDuplicateFileSkipped    = "DUPLICATE_FILE_SKIPPED"
	StatusPendingParsing          = "PENDING_PARSING"
	StatusContentDuplicateSkipped = "CONTENT_DUPLICATE_SKIPPED"
	StatusQueuedForLLM            = "QUEUED_FOR_LLM"
	StatusPendingLLM              = "PENDING_LLM"
	StatusChunkingFailed          = "CHUNKING_FAILED"
	StatusVectorizationFailed     = "VECTORIZATION_FAILED"
	StatusQdrantStoreFailed       = "QDRANT_STORE_FAILED" // New based on previous discussions
	StatusLLMProcessingFailed     = "LLM_PROCESSING_FAILED"
	StatusMatchFailed             = "MATCH_FAILED"
	StatusProcessingCompleted     = "PROCESSING_COMPLETED"
	StatusUploadProcessingFailed  = "UPLOAD_PROCESSING_FAILED" // New, from resume_handler
)

// Helper function to format keys with tenant and other parts
// This should ideally be in a redis utility or the storage.Redis itself
// For now, we define it here for clarity on how constants would be used.
// func FormatKey(tenantID, prefix string, parts ...string) string {
// 	 base := strings.Replace(prefix, TenantPlaceholder, tenantID, 1)
// 	 return base + strings.Join(parts, ":")
// }
