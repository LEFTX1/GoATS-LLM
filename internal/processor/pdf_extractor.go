package processor

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/parser"
	"context"
	"log"
	"time"
)

// BuildPDFExtractor 统一构建PDF解析器的逻辑
// 根据配置返回合适的PDF解析器实现
func BuildPDFExtractor(ctx context.Context, cfg *config.Config, loggerProvider func(prefix string) *log.Logger) (PDFExtractor, error) {
	initLogger := loggerProvider("[PDFExtractorInit] ")
	if cfg.Tika.Type == "tika" && cfg.Tika.ServerURL != "" {
		initLogger.Println("检测到Tika配置，正在初始化Tika PDF解析器...")
		var tikaOptions []parser.TikaOption
		if cfg.Tika.MetadataMode == "full" {
			tikaOptions = append(tikaOptions, parser.WithFullMetadata(true))
		} else if cfg.Tika.MetadataMode == "none" {
			tikaOptions = append(tikaOptions, parser.WithMinimalMetadata(false), parser.WithFullMetadata(false))
		} else { // "minimal" or default
			tikaOptions = append(tikaOptions, parser.WithMinimalMetadata(true))
		}
		if cfg.Tika.Timeout > 0 {
			tikaOptions = append(tikaOptions, parser.WithTimeout(time.Duration(cfg.Tika.Timeout)*time.Second))
		}
		tikaOptions = append(tikaOptions, parser.WithTikaLogger(loggerProvider("[TikaPDF] ")))
		return parser.NewTikaPDFExtractor(cfg.Tika.ServerURL, tikaOptions...), nil
	} else {
		initLogger.Println("未检测到Tika配置或配置不完整，将使用Eino作为PDF解析器...")
		return parser.NewEinoPDFTextExtractor(ctx, parser.WithEinoLogger(loggerProvider("[EinoPDF] ")))
	}
}
