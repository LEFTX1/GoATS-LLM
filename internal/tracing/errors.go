package tracing

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ErrorType 定义错误类型，便于分类和过滤
type ErrorType string

const (
	// ErrorTypeHTTP HTTP错误
	ErrorTypeHTTP ErrorType = "http"
	// ErrorTypeDB 数据库错误
	ErrorTypeDB ErrorType = "db"
	// ErrorTypeRedis Redis错误
	ErrorTypeRedis ErrorType = "redis"
	// ErrorTypeRabbitMQ RabbitMQ错误
	ErrorTypeRabbitMQ ErrorType = "rabbitmq"
	// ErrorTypeVectorDB 向量数据库错误
	ErrorTypeVectorDB ErrorType = "vector_db"
	// ErrorTypeValidation 验证错误
	ErrorTypeValidation ErrorType = "validation"
	// ErrorTypeInternal 内部错误
	ErrorTypeInternal ErrorType = "internal"
	// ErrorTypeExternal 外部系统错误
	ErrorTypeExternal ErrorType = "external_system"
	// ErrorTypeTimeout 超时错误
	ErrorTypeTimeout ErrorType = "timeout"
	// ErrorTypePermission 权限错误
	ErrorTypePermission ErrorType = "permission"
)

// RecordError 记录错误，添加统一的错误类型和详情
func RecordError(span trace.Span, err error, errorType ErrorType) {
	if span == nil || err == nil {
		return
	}

	// 记录错误
	span.RecordError(err)

	// 设置错误类型属性
	span.SetAttributes(
		attribute.String("error.type", string(errorType)),
		attribute.String("error.message", err.Error()),
	)

	// 设置span状态为错误
	span.SetStatus(codes.Error, err.Error())
}

// RecordErrorWithInfo 记录错误并添加额外信息
func RecordErrorWithInfo(span trace.Span, err error, errorType ErrorType, attributes ...attribute.KeyValue) {
	if span == nil || err == nil {
		return
	}

	// 记录错误
	span.RecordError(err)

	// 设置错误类型属性
	span.SetAttributes(
		attribute.String("error.type", string(errorType)),
		attribute.String("error.message", err.Error()),
	)

	// 设置额外属性
	if len(attributes) > 0 {
		span.SetAttributes(attributes...)
	}

	// 设置span状态为错误
	span.SetStatus(codes.Error, err.Error())
}

// RecordHTTPError 专门记录HTTP错误
func RecordHTTPError(span trace.Span, err error, statusCode int) {
	if span == nil || err == nil {
		return
	}

	// 记录错误
	span.RecordError(err)

	// 设置HTTP特定属性
	span.SetAttributes(
		attribute.String("error.type", string(ErrorTypeHTTP)),
		attribute.String("error.message", err.Error()),
		attribute.Int("http.status_code", statusCode),
	)

	// 根据HTTP状态码分类错误
	var errorCategory string
	switch {
	case statusCode >= 400 && statusCode < 500:
		errorCategory = "client_error"
	case statusCode >= 500:
		errorCategory = "server_error"
	default:
		errorCategory = "unknown"
	}

	span.SetAttributes(attribute.String("error.category", errorCategory))

	// 设置span状态为错误
	span.SetStatus(codes.Error, err.Error())
}

// RecordRabbitMQNack 记录RabbitMQ消息被拒绝的错误
func RecordRabbitMQNack(span trace.Span, messageID string, reason string) {
	if span == nil {
		return
	}

	errMsg := "message not acknowledged by broker"
	if reason != "" {
		errMsg = reason
	}

	// 设置RabbitMQ特定属性
	span.SetAttributes(
		attribute.String("error.type", string(ErrorTypeRabbitMQ)),
		attribute.String("error.message", errMsg),
		attribute.String("messaging.message_id", messageID),
		attribute.String("messaging.error_type", "nack"),
		attribute.Bool("messaging.rabbitmq.confirmed", false),
	)

	// 设置span状态为错误
	span.SetStatus(codes.Error, errMsg)
}

// RecordRabbitMQTimeout 记录RabbitMQ确认超时错误
func RecordRabbitMQTimeout(span trace.Span, messageID string, timeoutDuration string) {
	if span == nil {
		return
	}

	errMsg := "confirm timeout after " + timeoutDuration

	// 设置RabbitMQ特定属性
	span.SetAttributes(
		attribute.String("error.type", string(ErrorTypeRabbitMQ)),
		attribute.String("error.message", errMsg),
		attribute.String("messaging.message_id", messageID),
		attribute.String("messaging.error_type", "timeout"),
		attribute.Bool("messaging.rabbitmq.confirmed", false),
	)

	// 设置span状态为错误
	span.SetStatus(codes.Error, errMsg)
}
