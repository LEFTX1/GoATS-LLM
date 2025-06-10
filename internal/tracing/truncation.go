package tracing

import (
	"strings"
)

const (
	// DefaultMaxLength 默认最大属性长度
	DefaultMaxLength = 200

	// MaxSQLLength SQL语句最大长度
	MaxSQLLength = 500

	// MaxRedisLength Redis键值最大长度
	MaxRedisLength = 100

	// MaxQdrantLength Qdrant向量内容最大长度
	MaxQdrantLength = 100

	// MaxHeaderLength HTTP头最大长度
	MaxHeaderLength = 100

	// MaxResumeLength 简历内容最大长度
	MaxResumeLength = 150
)

// maskPIILookup 需要掩码处理的关键字映射
var maskPIILookup = map[string]bool{
	"email":    true,
	"phone":    true,
	"password": true,
	"身份证":      true,
	"id_card":  true,
	"address":  true,
	"地址":       true,
	"name":     true,
	"姓名":       true,
	"age":      true,
	"年龄":       true,
	"secret":   true,
	"token":    true,
}

// SafeAttributeValue 确保属性值安全，不包含敏感信息
// 1. 如果是敏感关键字对应的值，返回掩码处理后的值
// 2. 如果长度超过maxLength，则截断并添加省略号
func SafeAttributeValue(name string, value string, maxLength int) string {
	// 检查是否包含需要掩码的关键字
	lowerName := strings.ToLower(name)
	for keyword := range maskPIILookup {
		if strings.Contains(lowerName, keyword) {
			return MaskPII(value)
		}
	}

	// 截断过长的值
	return TruncateString(value, maxLength)
}

// MaskPII 对个人敏感信息进行掩码处理
func MaskPII(value string) string {
	if value == "" {
		return ""
	}

	runes := []rune(value)
	length := len(runes)

	if length <= 1 {
		return "*"
	}
	// Handles short names like "张三" (len=2) -> "张*", "王小明" (len=3) -> "王*明"
	if length <= 4 {
		if length == 2 {
			return string(runes[0:1]) + "*"
		}
		return string(runes[0:1]) + strings.Repeat("*", length-2) + string(runes[length-1:])
	}

	// Handles longer strings like emails and phone numbers. Keep first 2 and last 2.
	// "myemail@example.com" -> "my***************om"
	// "13812345678" -> "13*******78"
	return string(runes[0:2]) + strings.Repeat("*", length-4) + string(runes[length-2:])
}

// TruncateString 截断字符串，并在截断时添加省略号
func TruncateString(s string, maxLength int) string {
	runes := []rune(s)
	if len(runes) <= maxLength {
		return s
	}

	if maxLength <= 3 {
		return string(runes[:maxLength])
	}

	half := (maxLength - 3) / 2
	if half < 1 {
		half = 1
	}

	// 保留前后部分，中间用...连接
	return string(runes[:half]) + "..." + string(runes[len(runes)-half:])
}

// SafeSQL 安全处理SQL语句
func SafeSQL(sql string) string {
	return TruncateString(sql, MaxSQLLength)
}

// SafeRedisKey 安全处理Redis键
func SafeRedisKey(key string) string {
	return TruncateString(key, MaxRedisLength)
}

// SafeResumeContent 安全处理简历内容
func SafeResumeContent(content string) string {
	return TruncateString(content, MaxResumeLength)
}
