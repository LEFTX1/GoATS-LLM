package logger // 定义了日志记录器相关的组件和功能

import (
	"context" // 导入上下文包，用于在日志中传递请求范围的数据
	"io"      // 导入I/O接口包
	"os"      // 导入操作系统功能包，如此处的标准输出
	"path/filepath"
	"time" // 导入时间包，用于格式化时间戳

	"github.com/rs/zerolog"     // 导入高性能的zerolog日志库
	"github.com/rs/zerolog/log" // 导入zerolog的全局日志实例
	"go.opentelemetry.io/otel/trace"
)

var (
	// Logger 默认的全局日志实例，应用中其他地方可以直接使用
	Logger = log.Logger
)

// 定义context key
type loggerContextKey struct{}

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
	var output io.Writer
	logFilePath := "logs/app.log" // Default path

	if config.Format == "pretty" { // 如果格式是"pretty"
		output = zerolog.ConsoleWriter{ // 使用zerolog的控制台写入器
			Out:        os.Stdout,         // 输出目标为标准输出
			TimeFormat: config.TimeFormat, // 使用配置的时间格式
			NoColor:    false,             // 在控制台输出中启用颜色
		}
	} else {
		// 默认或指定为json等其他格式时，输出到文件
		// 在写入文件之前，确保目录存在
		logDir := filepath.Dir(logFilePath)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			log.Fatal().Err(err).Str("path", logDir).Msg("创建日志目录失败")
		}

		file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
		if err != nil {
			log.Fatal().Err(err).Str("path", logFilePath).Msg("无法打开日志文件")
		}
		output = file
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

// WithOTelTrace 从trace上下文提取trace_id和span_id添加到日志
func WithOTelTrace(ctx context.Context) (context.Context, *zerolog.Logger) {
	// 先从上下文中获取可能已有的日志器及其字段
	var baseLogger *zerolog.Logger
	if ctx != nil {
		// 首先尝试从我们的自定义key中获取
		if l, ok := ctx.Value(loggerContextKey{}).(*zerolog.Logger); ok {
			baseLogger = l
		} else {
			// 如果没有，尝试从zerolog的内置上下文中获取
			l := zerolog.Ctx(ctx)
			if l != zerolog.DefaultContextLogger {
				baseLogger = l
			}
		}
	}

	// 如果上下文中没有logger，则使用全局logger作为基础
	if baseLogger == nil {
		baseLogger = &Logger
	}

	// 从trace上下文获取span信息
	spanCtx := trace.SpanContextFromContext(ctx)

	// 创建新的logger，保留原有字段的同时添加trace信息
	var enrichedLogger zerolog.Logger
	if spanCtx.IsValid() {
		// 有效trace上下文，添加trace信息
		enrichedLogger = baseLogger.With().
			Str("trace_id", spanCtx.TraceID().String()).
			Str("span_id", spanCtx.SpanID().String()).
			Logger()
	} else {
		// 无效trace上下文，保持原有logger
		enrichedLogger = *baseLogger
	}

	// 同时更新我们的自定义key和zerolog的内置上下文
	newCtx := context.WithValue(ctx, loggerContextKey{}, &enrichedLogger)
	newCtx = enrichedLogger.WithContext(newCtx)

	return newCtx, &enrichedLogger
}

// WithSubmissionUUID 增强版，同时添加submission_uuid和trace信息
func WithSubmissionUUID(ctx context.Context, submissionUUID string) context.Context {
	// 先检查ctx中是否已缓存了logger
	var baseLogger *zerolog.Logger
	if ctx != nil {
		if l, ok := ctx.Value(loggerContextKey{}).(*zerolog.Logger); ok {
			baseLogger = l
		}
	}

	// 没有缓存的logger，尝试创建一个带trace信息的logger
	if baseLogger == nil {
		spanCtx := trace.SpanContextFromContext(ctx)
		if spanCtx.IsValid() {
			logger := Logger.With().
				Str("trace_id", spanCtx.TraceID().String()).
				Str("span_id", spanCtx.SpanID().String()).
				Logger()
			baseLogger = &logger
		} else {
			baseLogger = &Logger
		}
	}

	// 在已有logger基础上添加submission_uuid
	enrichedLogger := baseLogger.With().Str("submission_uuid", submissionUUID).Logger()

	// 保存到context
	return context.WithValue(ctx, loggerContextKey{}, &enrichedLogger)
}

// FromContext 增强版，优先从context获取带trace的logger
func FromContext(ctx context.Context) *zerolog.Logger {
	// 先尝试从上下文中获取缓存的logger
	if ctx != nil {
		if l, ok := ctx.Value(loggerContextKey{}).(*zerolog.Logger); ok {
			return l
		}
	}

	// 无缓存logger，检查是否有trace信息并创建新logger
	var logger zerolog.Logger
	if ctx != nil {
		spanCtx := trace.SpanContextFromContext(ctx)
		if spanCtx.IsValid() {
			logger = Logger.With().
				Str("trace_id", spanCtx.TraceID().String()).
				Str("span_id", spanCtx.SpanID().String()).
				Logger()

			// 缓存新创建的logger到context，以便后续调用复用
			if ctx != nil {
				ctx = context.WithValue(ctx, loggerContextKey{}, &logger)
			}
			return &logger
		}
	}

	// 无trace信息，返回默认logger
	return &Logger
}
