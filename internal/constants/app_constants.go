package constants

import "time"

const (
	// Application-level constants
	DefaultParserVer = "1.0" // Placeholder for actual Tika/LLM versions

	// Storage-related constants (moved from internal/storage/constants.go)
	JDCachePrefix       = "jd_text:"
	JDCacheDuration     = 24 * time.Hour
	RawFileMD5SetKey    = "resumes:file_md5s" // Redis Set key for storing raw file MD5s
	ParsedTextMD5SetKey = "resumes:text_md5s" // Redis Set key for storing parsed text MD5s
)
