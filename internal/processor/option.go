package processor

import (
	"ai-agent-go/internal/storage"
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
		compOpts = append(compOpts, WithComp_PDFExtractor(tempProcessor.PDFExtractor))
	}
	if tempProcessor.ResumeChunker != nil {
		compOpts = append(compOpts, WithComp_ResumeChunker(tempProcessor.ResumeChunker))
	}
	if tempProcessor.ResumeEmbedder != nil {
		compOpts = append(compOpts, WithComp_ResumeEmbedder(tempProcessor.ResumeEmbedder))
	}
	if tempProcessor.MatchEvaluator != nil {
		compOpts = append(compOpts, WithComp_JobMatchEvaluator(tempProcessor.MatchEvaluator))
	}
	if tempProcessor.Storage != nil {
		compOpts = append(compOpts, WithComp_Storage(tempProcessor.Storage))
	}

	// 创建对应的新设置选项
	var setOpts []SettingOpt
	setOpts = append(setOpts, WithSet_UseLLM(tempProcessor.Config.UseLLM))
	setOpts = append(setOpts, WithSet_DefaultDimensions(tempProcessor.Config.DefaultDimensions))
	setOpts = append(setOpts, WithSet_Debug(tempProcessor.Config.Debug))
	if tempProcessor.Config.Logger != nil {
		setOpts = append(setOpts, WithSet_Logger(tempProcessor.Config.Logger))
	}

	return compOpts, setOpts
}

// ----- 新版组件选项 -----

// WithComp_PDFExtractor 设置PDF提取器组件
func WithComp_PDFExtractor(extractor PDFExtractor) ComponentOpt {
	return func(c *Components) {
		c.PDFExtractor = extractor
	}
}

// WithComp_ResumeChunker 设置简历分块器组件
func WithComp_ResumeChunker(chunker ResumeChunker) ComponentOpt {
	return func(c *Components) {
		c.ResumeChunker = chunker
	}
}

// WithComp_ResumeEmbedder 设置简历嵌入器组件
func WithComp_ResumeEmbedder(embedder ResumeEmbedder) ComponentOpt {
	return func(c *Components) {
		c.ResumeEmbedder = embedder
	}
}

// WithComp_JobMatchEvaluator 设置岗位匹配评估器组件
func WithComp_JobMatchEvaluator(evaluator JobMatchEvaluator) ComponentOpt {
	return func(c *Components) {
		c.MatchEvaluator = evaluator
	}
}

// WithComp_Storage 设置存储组件
func WithComp_Storage(storage *storage.Storage) ComponentOpt {
	return func(c *Components) {
		c.Storage = storage
	}
}

// ----- 新版设置选项 -----

// WithSet_Debug 设置调试模式
func WithSet_Debug(debug bool) SettingOpt {
	return func(s *Settings) {
		s.Debug = debug
	}
}

// WithSet_Logger 设置日志记录器
func WithSet_Logger(logger *log.Logger) SettingOpt {
	return func(s *Settings) {
		if logger != nil {
			s.Logger = logger
		} else {
			// 提供一个 discard logger 以防万一
			s.Logger = log.New(log.Writer(), "[NilLoggerFallback] ", log.LstdFlags)
		}
	}
}

// WithSet_DefaultDimensions 设置默认向量维度
func WithSet_DefaultDimensions(dimensions int) SettingOpt {
	return func(s *Settings) {
		s.DefaultDimensions = dimensions
	}
}

// WithSet_UseLLM 设置是否使用LLM
func WithSet_UseLLM(useLLM bool) SettingOpt {
	return func(s *Settings) {
		s.UseLLM = useLLM
	}
}

// WithSet_TimeLocation 设置时区
func WithSet_TimeLocation(loc *time.Location) SettingOpt {
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
