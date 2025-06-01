package processor

import "log"

// ProcessorOption 处理器选项函数类型
type ProcessorOption func(*ResumeProcessor)

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
