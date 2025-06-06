package logger

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	// Logger 默认的全局日志实例
	Logger = log.Logger
)

// Config 日志配置
type Config struct {
	Level        string `json:"level" yaml:"level"`                 // 日志级别：debug, info, warn, error
	Format       string `json:"format" yaml:"format"`               // 日志格式：json, pretty
	TimeFormat   string `json:"time_format" yaml:"time_format"`     // 时间格式
	ReportCaller bool   `json:"report_caller" yaml:"report_caller"` // 是否报告调用者信息
}

// Init 初始化日志系统
func Init(config Config) {
	// 设置日志级别
	level, err := zerolog.ParseLevel(config.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)

	// 设置日志格式
	var output io.Writer = os.Stdout
	if config.Format == "pretty" {
		output = zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: config.TimeFormat,
			NoColor:    false,
		}
	}

	// 设置默认时间格式
	if config.TimeFormat == "" {
		zerolog.TimeFieldFormat = time.RFC3339
	} else {
		zerolog.TimeFieldFormat = config.TimeFormat
	}

	// 创建日志记录器
	contextLogger := zerolog.New(output).
		Level(level).
		With().
		Timestamp()

	// 是否添加调用者信息
	if config.ReportCaller {
		contextLogger = contextLogger.Caller()
	}

	// 替换全局日志记录器
	Logger = contextLogger.Logger()
	log.Logger = Logger
}

// Debug 调试级别的日志记录
func Debug() *zerolog.Event {
	return Logger.Debug()
}

// Info 信息级别的日志记录
func Info() *zerolog.Event {
	return Logger.Info()
}

// Warn 警告级别的日志记录
func Warn() *zerolog.Event {
	return Logger.Warn()
}

// Error 错误级别的日志记录
func Error() *zerolog.Event {
	return Logger.Error()
}

// Fatal 致命错误级别的日志记录，会导致程序退出
func Fatal() *zerolog.Event {
	return Logger.Fatal()
}

// Ctx 使用上下文的日志记录
func Ctx(ctx context.Context) *zerolog.Logger {
	return zerolog.Ctx(ctx)
}

// WithContext 将zerolog记录器添加到上下文
func WithContext(ctx context.Context) context.Context {
	return Logger.WithContext(ctx)
}
