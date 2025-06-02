package utils

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"time"

	"gorm.io/datatypes"
)

// StringPtr 返回字符串的指针
func StringPtr(s string) *string {
	if s == "" {
		// Depending on DB schema, you might want to return nil for empty strings
		// For now, returning pointer to empty string
	}
	return &s
}

// TimePtr returns a pointer to a time.Time object
func TimePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// IntPtr returns a pointer to an int
func IntPtr(i int) *int {
	return &i
}

// CalculateMD5 computes the MD5 hash of a byte slice.
func CalculateMD5(data []byte) string {
	hasher := md5.New()
	hasher.Write(data)
	return hex.EncodeToString(hasher.Sum(nil))
}

// ConvertArrayToJSON 辅助函数: 将字符串数组转换为JSON
func ConvertArrayToJSON(arr []string) datatypes.JSON {
	if arr == nil || len(arr) == 0 {
		return datatypes.JSON("[]")
	}

	jsonBytes, err := json.Marshal(arr)
	if err != nil {
		// 对于处理简单数组的辅助函数，通常返回一个安全的默认值或记录错误
		// 而不是直接 panic 或返回 error，除非调用者明确需要处理错误。
		// 在这种情况下，返回空的JSON数组是一个合理的默认值。
		return datatypes.JSON("[]")
	}

	return datatypes.JSON(jsonBytes)
}
