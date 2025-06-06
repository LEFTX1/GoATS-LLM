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
