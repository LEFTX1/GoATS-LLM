package logger // 定义了日志记录器相关的组件和功能

import (
	"context" // 导入上下文包，用于在日志中传递请求范围的数据
	"io"      // 导入I/O接口包
	"os"      // 导入操作系统功能包，如此处的标准输出
	"time"    // 导入时间包，用于格式化时间戳

	"github.com/rs/zerolog"     // 导入高性能的zerolog日志库
	"github.com/rs/zerolog/log" // 导入zerolog的全局日志实例
)

var (
	// Logger 默认的全局日志实例，应用中其他地方可以直接使用
	Logger = log.Logger
)

// Config 日志配置结构体，用于定义日志系统的行为
type Config struct {
	Level        string `json:"level" yaml:"level"`                 // 日志级别：debug, info, warn, error等
	Format       string `json:"format" yaml:"format"`               // 日志格式：json（机器可读）或 pretty（人类可读的控制台格式）
	TimeFormat   string `json:"time_format" yaml:"time_format"`     // 时间戳的格式
	ReportCaller bool   `json:"report_caller" yaml:"report_caller"` // 是否在日志中报告调用者的文件名和行号
}

// Init 初始化日志系统，根据传入的配置进行设置
func Init(config Config) {
	// 设置日志级别
	level, err := zerolog.ParseLevel(config.Level) // 解析字符串格式的日志级别
	if err != nil {                                // 如果解析失败
		level = zerolog.InfoLevel // 默认使用Info级别
	}
	zerolog.SetGlobalLevel(level) // 设置全局日志级别

	// 设置日志输出格式
	var output io.Writer = os.Stdout // 默认输出到标准输出
	if config.Format == "pretty" {   // 如果格式是"pretty"
		output = zerolog.ConsoleWriter{ // 使用zerolog的控制台写入器
			Out:        os.Stdout,         // 输出目标为标准输出
			TimeFormat: config.TimeFormat, // 使用配置的时间格式
			NoColor:    false,             // 在控制台输出中启用颜色
		}
	}

	// 设置默认时间格式
	if config.TimeFormat == "" { // 如果未指定时间格式
		zerolog.TimeFieldFormat = time.RFC3339 // 使用RFC3339标准格式
	} else {
		zerolog.TimeFieldFormat = config.TimeFormat // 使用配置中指定的格式
	}

	// 创建日志记录器实例
	contextLogger := zerolog.New(output). // 基于指定的输出创建一个新的logger
						Level(level). // 设置其日志级别
						With().       // 开始构建logger的上下文
						Timestamp()   // 在每条日志中自动添加时间戳字段

	// 是否添加调用者信息
	if config.ReportCaller { // 如果配置要求报告调用者信息
		contextLogger = contextLogger.Caller() // 在logger上下文中添加调用者信息（文件:行号）
	}

	// 替换全局日志记录器
	Logger = contextLogger.Logger() // 完成logger构建并赋值给包内全局变量
	log.Logger = Logger             // 同时替换zerolog库的全局logger
}

// Debug 开始一条调试级别的日志事件
func Debug() *zerolog.Event {
	return Logger.Debug()
}

// Info 开始一条信息级别的日志事件
func Info() *zerolog.Event {
	return Logger.Info()
}

// Warn 开始一条警告级别的日志事件
func Warn() *zerolog.Event {
	return Logger.Warn()
}

// Error 开始一条错误级别的日志事件
func Error() *zerolog.Event {
	return Logger.Error()
}

// Fatal 开始一条致命错误级别的日志事件，记录后程序将退出
func Fatal() *zerolog.Event {
	return Logger.Fatal()
}

// Ctx 从上下文中获取日志记录器（如果存在）
func Ctx(ctx context.Context) *zerolog.Logger {
	return zerolog.Ctx(ctx) // zerolog内置的辅助函数，用于从context中提取logger
}

// WithContext 将全局日志记录器添加到上下文中，并返回一个新的上下文
func WithContext(ctx context.Context) context.Context {
	return Logger.WithContext(ctx) // 返回一个包含了该logger的新context
}
