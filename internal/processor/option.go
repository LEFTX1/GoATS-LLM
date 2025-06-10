package processor

import (
	"ai-agent-go/internal/storage"
	"fmt"
	"io"
	"log"
	"time"
)

// ProcessorOption 处理器选项函数类型 (旧版，将逐步淘汰)
type ProcessorOption func(*ResumeProcessor)

// ComponentOpt 组件选项类型，仅改变 Components 结构体内的字段
type ComponentOpt func(*Components)

// SettingOpt 设置选项类型，仅改变 Settings 结构体内的字段
type SettingOpt func(*Settings)

// ----- 辅助函数 -----

// ConvertOldOptions 将旧式 ProcessorOption 转换为 ComponentOpt 和 SettingOpt
// 此函数用于帮助迁移旧代码到新的选项模式
func ConvertOldOptions(oldOpts []ProcessorOption) ([]ComponentOpt, []SettingOpt) {
	// 创建一个临时的 ResumeProcessor 来接收旧的选项
	tempProcessor := &ResumeProcessor{
		Config: ComponentConfig{
			UseLLM:            true,
			DefaultDimensions: 1536,
			Debug:             false,
			Logger:            log.New(log.Writer(), "[TempProcessor] ", log.LstdFlags),
		},
	}

	// 应用所有旧选项
	for _, opt := range oldOpts {
		opt(tempProcessor)
	}

	// 创建对应的新组件选项
	var compOpts []ComponentOpt
	if tempProcessor.PDFExtractor != nil {
		compOpts = append(compOpts, WithcompPdfextractor(tempProcessor.PDFExtractor))
	}
	if tempProcessor.ResumeChunker != nil {
		compOpts = append(compOpts, WithcompResumechunker(tempProcessor.ResumeChunker))
	}
	if tempProcessor.ResumeEmbedder != nil {
		compOpts = append(compOpts, WithcompResumeembedder(tempProcessor.ResumeEmbedder))
	}
	if tempProcessor.MatchEvaluator != nil {
		compOpts = append(compOpts, WithcompJobmatchevaluator(tempProcessor.MatchEvaluator))
	}
	if tempProcessor.Storage != nil {
		compOpts = append(compOpts, WithcompStorage(tempProcessor.Storage))
	}

	// 创建对应的新设置选项
	var setOpts []SettingOpt
	setOpts = append(setOpts, WithsetUsellm(tempProcessor.Config.UseLLM))
	setOpts = append(setOpts, WithsetDefaultdimensions(tempProcessor.Config.DefaultDimensions))
	setOpts = append(setOpts, WithsetDebug(tempProcessor.Config.Debug))
	if tempProcessor.Config.Logger != nil {
		setOpts = append(setOpts, WithsetLogger(tempProcessor.Config.Logger))
	}

	return compOpts, setOpts
}

// ----- 新版组件选项 -----

// WithcompPdfextractor 设置PDF提取器组件
func WithcompPdfextractor(extractor PDFExtractor) ComponentOpt {
	return func(c *Components) {
		c.PDFExtractor = extractor
	}
}

// WithcompResumechunker 设置简历分块器组件
func WithcompResumechunker(chunker ResumeChunker) ComponentOpt {
	return func(c *Components) {
		c.ResumeChunker = chunker
	}
}

// WithcompResumeembedder 设置简历嵌入器组件
func WithcompResumeembedder(embedder ResumeEmbedder) ComponentOpt {
	return func(c *Components) {
		c.ResumeEmbedder = embedder
	}
}

// WithcompJobmatchevaluator 设置岗位匹配评估器组件
func WithcompJobmatchevaluator(evaluator JobMatchEvaluator) ComponentOpt {
	return func(c *Components) {
		c.MatchEvaluator = evaluator
	}
}

// WithcompStorage 设置存储组件
func WithcompStorage(storage *storage.Storage) ComponentOpt {
	return func(c *Components) {
		c.Storage = storage
	}
}

// ----- 新版设置选项 -----

// WithsetDebug 设置调试模式
func WithsetDebug(debug bool) SettingOpt {
	return func(s *Settings) {
		s.Debug = debug
	}
}

// WithsetLogger 设置日志记录器
func WithsetLogger(logger *log.Logger) SettingOpt {
	return func(s *Settings) {
		if logger != nil {
			s.Logger = logger
		} else {
			// 提供一个 discard logger 以防万一
			s.Logger = log.New(log.Writer(), "[NilLoggerFallback] ", log.LstdFlags)
		}
	}
}

// WithsetDefaultdimensions 设置默认向量维度
func WithsetDefaultdimensions(dimensions int) SettingOpt {
	return func(s *Settings) {
		s.DefaultDimensions = dimensions
	}
}

// WithsetUsellm 设置是否使用LLM
func WithsetUsellm(useLLM bool) SettingOpt {
	return func(s *Settings) {
		s.UseLLM = useLLM
	}
}

// WithsetTimelocation 设置时区
func WithsetTimelocation(loc *time.Location) SettingOpt {
	return func(s *Settings) {
		if loc != nil {
			s.TimeLocation = loc
		} else {
			s.TimeLocation = time.Local
		}
	}
}

// ----- 旧版选项(保留向后兼容) -----

// WithPDFExtractor 设置PDF提取器
func WithPDFExtractor(extractor PDFExtractor) ProcessorOption {
	return func(p *ResumeProcessor) {
		p.PDFExtractor = extractor
	}
}

// WithResumeChunker 设置简历分块器
func WithResumeChunker(chunker ResumeChunker) ProcessorOption {
	return func(p *ResumeProcessor) {
		p.ResumeChunker = chunker
	}
}

// WithResumeEmbedder 设置简历嵌入器
func WithResumeEmbedder(embedder ResumeEmbedder) ProcessorOption {
	return func(p *ResumeProcessor) {
		p.ResumeEmbedder = embedder
	}
}

// WithJobMatchEvaluator 设置岗位匹配评估器
func WithJobMatchEvaluator(evaluator JobMatchEvaluator) ProcessorOption {
	return func(p *ResumeProcessor) {
		p.MatchEvaluator = evaluator
	}
}

// WithDebugMode 设置调试模式
func WithDebugMode(debug bool) ProcessorOption {
	return func(p *ResumeProcessor) {
		p.Config.Debug = debug
	}
}

// WithLogger 设置自定义日志记录器
func WithLogger(logger *log.Logger) ProcessorOption {
	return func(p *ResumeProcessor) {
		p.Config.Logger = logger
	}
}

// WithDefaultDimensions 设置默认向量维度
func WithDefaultDimensions(dimensions int) ProcessorOption {
	return func(p *ResumeProcessor) {
		p.Config.DefaultDimensions = dimensions
	}
}

// ProcessorOption 是 ResumeProcessor 的配置选项
// type ProcessorOption func(*ResumeProcessor)

// WithStorage 是一个选项，用于设置 ResumeProcessor 的 Storage 依赖
func WithStorage(s *storage.Storage) ProcessorOption {
	return func(rp *ResumeProcessor) { // 返回一个配置函数
		rp.Storage = s // 将传入的storage实例赋值给ResumeProcessor
	}
}

// WithProcessorLogger sets the logger for the ResumeProcessor.
// This directly sets the logger in the ComponentConfig.
func WithProcessorLogger(logger *log.Logger) ProcessorOption { // 为ResumeProcessor设置日志记录器
	return func(rp *ResumeProcessor) { // 返回一个配置函数
		if logger != nil { // 如果传入的logger不为空
			rp.Config.Logger = logger // 则设置为该logger
		} else { // 否则
			// Fallback to a discard logger if nil is passed, to prevent panics.
			rp.Config.Logger = log.New(io.Discard, "[ResumeProcessorNilLoggerFallback] ", log.LstdFlags) // 使用一个丢弃所有日志的logger，防止panic
		}
	}
}

// LogDebug 记录调试日志
func (rp *ResumeProcessor) LogDebug(message string) {
	if rp.Config.Debug && rp.Config.Logger != nil { // 如果开启了调试模式且logger不为空
		rp.Config.Logger.Println(message) // 则记录日志
	}
}

// 新增统一的日志包装方法
// logDebug 记录调试级别日志
func (rp *ResumeProcessor) logDebug(format string, args ...interface{}) {
	if rp.Config.Debug && rp.Config.Logger != nil {
		rp.Config.Logger.Printf(format, args...)
	}
}

// logInfo 记录信息级别日志
func (rp *ResumeProcessor) logInfo(format string, args ...interface{}) {
	if rp.Config.Logger != nil {
		rp.Config.Logger.Printf(format, args...)
	}
}

// logWarn 记录警告级别日志
func (rp *ResumeProcessor) logWarn(format string, args ...interface{}) {
	if rp.Config.Logger != nil {
		rp.Config.Logger.Printf("[WARN] "+format, args...)
	}
}

// logError 记录错误级别日志
func (rp *ResumeProcessor) logError(err error, format string, args ...interface{}) {
	if rp.Config.Logger != nil {
		// 如果提供了错误对象，先添加错误信息
		if err != nil {
			format = fmt.Sprintf("ERROR: %v - %s", err, format)
		} else {
			format = "ERROR: " + format
		}
		rp.Config.Logger.Printf(format, args...)
	}
}
