package processor

import "log"

// JDOption 定义了 JDProcessor 的配置选项函数类型。
type JDOption func(*JDProcessor)

// WithJDEmbedder 设置 JDProcessor 使用的 TextEmbedder。
func WithJDEmbedder(embedder TextEmbedder) JDOption {
	return func(p *JDProcessor) {
		p.embedder = embedder
	}
}

// WithJDProcessorLogger 设置 JDProcessor 使用的日志记录器。
func WithJDProcessorLogger(logger *log.Logger) JDOption {
	return func(p *JDProcessor) {
		p.logger = logger
	}
}
