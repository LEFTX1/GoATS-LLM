package parser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/document/parser/pdf"
	einoParser "github.com/cloudwego/eino/components/document/parser"
	// "github.com/cloudwego/eino/schema" // schema.Document is implicitly handled by pdf.PDFParser's return type
)

// EinoPDFTextExtractor 使用 Eino PDF Parser 提取文本
type EinoPDFTextExtractor struct {
	parser *pdf.PDFParser
	logger *log.Logger
}

// EinoPDFOption PDF提取器的配置选项
type EinoPDFOption func(*EinoPDFTextExtractor)

// WithEinoLogger 配置自定义日志记录器 (导出)
func WithEinoLogger(logger *log.Logger) EinoPDFOption {
	return func(e *EinoPDFTextExtractor) {
		e.logger = logger
	}
}

// NewEinoPDFTextExtractor 初始化 Eino PDF 文本提取器
// 默认配置为不按页面分割，以获取整个文档的连续文本
func NewEinoPDFTextExtractor(ctx context.Context, options ...EinoPDFOption) (*EinoPDFTextExtractor, error) {
	p, err := pdf.NewPDFParser(ctx, &pdf.Config{
		ToPages: false, // 非常重要：我们希望获取整个PDF的文本作为单个字符串
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Eino PDF parser: %w", err)
	}

	extractor := &EinoPDFTextExtractor{
		parser: p,
		logger: log.New(os.Stderr, "[PDF解析器] ", log.LstdFlags),
	}

	// 应用选项
	for _, option := range options {
		option(extractor)
	}

	return extractor, nil
}

// ExtractFromFile 实现processor.PDFExtractor接口，从PDF文件提取文本
func (e *EinoPDFTextExtractor) ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error) {
	return e.ExtractFullTextFromPDFFile(ctx, filePath)
}

// ExtractFullTextFromPDFFile 从给定的PDF文件路径中提取完整的纯文本内容和元数据
// 返回: 提取的文本内容 (string), 解析器元数据 (map[string]interface{}), 错误 (error)
func (e *EinoPDFTextExtractor) ExtractFullTextFromPDFFile(ctx context.Context, filePath string) (string, map[string]interface{}, error) {
	startTime := time.Now()
	e.logger.Printf("开始处理PDF文件: %s", filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return "", nil, fmt.Errorf("failed to open PDF file %s: %w", filePath, err)
	}
	defer file.Close()

	// 获取文件大小，用于日志记录
	fileInfo, err := file.Stat()
	if err == nil {
		e.logger.Printf("PDF文件大小: %.2f MB", float64(fileInfo.Size())/1024/1024)
	}

	// 创建metadata作为interface{}传递
	extraMeta := map[string]interface{}{
		"source_file_path": filePath,
		"extraction_time":  time.Now().Format(time.RFC3339),
	}

	text, metadata, err := e.ExtractTextFromReader(ctx, file, filePath, extraMeta)

	duration := time.Since(startTime)
	if err != nil {
		e.logger.Printf("PDF处理失败: %s (用时 %.2f秒)", err, duration.Seconds())
		return "", nil, err
	}

	e.logger.Printf("PDF处理完成: 提取了 %d 个字符 (用时 %.2f秒)", len(text), duration.Seconds())
	return text, metadata, nil
}

// ExtractTextFromReader 从 io.Reader 中提取文本 (更通用的版本)
// 返回: 提取的文本内容 (string), 解析器元数据 (map[string]interface{}), 错误 (error)
func (e *EinoPDFTextExtractor) ExtractTextFromReader(ctx context.Context, reader io.Reader, uri string, options interface{}) (string, map[string]interface{}, error) {
	// 将options转换为map[string]interface{}
	var extraMeta map[string]interface{}
	if options != nil {
		if meta, ok := options.(map[string]interface{}); ok {
			extraMeta = meta
		} else {
			// 如果不是期望的类型，创建一个新的并记录原始options
			extraMeta = map[string]interface{}{
				"original_options": options,
			}
		}
	} else {
		extraMeta = make(map[string]interface{})
	}

	startTime := time.Now()
	e.logger.Printf("开始从Reader提取PDF文本 (URI: %s)", uri)

	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second) // 30秒超时
	defer cancel()

	docs, err := e.parser.Parse(ctx, reader,
		einoParser.WithURI(uri),
		einoParser.WithExtraMeta(extraMeta),
	)

	duration := time.Since(startTime)
	if err != nil {
		e.logger.Printf("从Reader提取PDF失败: %s (用时 %.2f秒)", err, duration.Seconds())
		return "", extraMeta, fmt.Errorf("eino PDF parser failed for URI %s: %w", uri, err)
	}

	if len(docs) == 0 {
		e.logger.Printf("PDF解析无结果 (用时 %.2f秒)", duration.Seconds())
		return "", extraMeta, fmt.Errorf("eino PDF parser returned no documents for URI %s", uri)
	}

	if len(docs) > 1 {
		e.logger.Printf("注意: 返回了多个文档 (%d) (用时 %.2f秒)", len(docs), duration.Seconds())
	}

	// 合并所有文档的内容（以防万一返回了多个）
	var fullContent string
	for i, doc := range docs {
		fullContent += doc.Content
		if i < len(docs)-1 {
			fullContent += "\n\n--- Page Break (inferred) ---\n\n" // 如果有多个文档，模拟分页符
		}
	}

	// 合并元数据
	var finalMetadata map[string]interface{}
	if len(docs) > 0 && docs[0].MetaData != nil {
		finalMetadata = docs[0].MetaData
	} else {
		finalMetadata = make(map[string]interface{}) // 确保返回非nil map
	}

	// 确保我们添加的元数据存在
	for k, v := range extraMeta {
		finalMetadata[k] = v
	}

	// 添加处理时间
	finalMetadata["processing_duration_ms"] = duration.Milliseconds()
	finalMetadata["document_count"] = len(docs)
	finalMetadata["text_length"] = len(fullContent)

	e.logger.Printf("PDF提取完成: 提取了 %d 个字符 (用时 %.2f秒)", len(fullContent), duration.Seconds())
	return fullContent, finalMetadata, nil
}

// ExtractTextFromBytes 从字节数组提取文本内容
func (e *EinoPDFTextExtractor) ExtractTextFromBytes(ctx context.Context, data []byte, uri string, options interface{}) (string, map[string]interface{}, error) {
	reader := bytes.NewReader(data)

	// 将options转换为map[string]interface{}（如果可能）
	var extraMeta map[string]interface{}
	if options != nil {
		if meta, ok := options.(map[string]interface{}); ok {
			extraMeta = meta
		} else {
			// 如果不是期望的类型，创建一个新的并记录原始options
			extraMeta = map[string]interface{}{
				"original_options": options,
			}
		}
	}

	return e.ExtractTextFromReader(ctx, reader, uri, extraMeta)
}

// ExtractStructuredContent 提取结构化内容（如段落、表格等）
func (e *EinoPDFTextExtractor) ExtractStructuredContent(ctx context.Context, reader io.Reader, uri string, options interface{}) (map[string]interface{}, error) {
	// 目前Eino还不完全支持结构化内容提取，但我们可以提供基本实现
	e.logger.Printf("尝试提取结构化内容 (URI: %s)", uri)

	// 将options转换为map[string]interface{}（如果可能）
	var extraMeta map[string]interface{}
	if options != nil {
		if meta, ok := options.(map[string]interface{}); ok {
			extraMeta = meta
		} else {
			extraMeta = map[string]interface{}{
				"original_options": options,
			}
		}
	}

	// 创建带超时的上下文
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second) // 30秒超时
	defer cancel()

	// 使用eino parser解析文档
	docs, err := e.parser.Parse(ctx, reader,
		einoParser.WithURI(uri),
		einoParser.WithExtraMeta(extraMeta),
	)

	if err != nil {
		return nil, fmt.Errorf("结构化内容提取失败: %w", err)
	}

	if len(docs) == 0 {
		return nil, fmt.Errorf("eino PDF parser returned no documents for URI %s", uri)
	}

	// 构建结构化响应
	result := make(map[string]interface{})

	// 基本文档信息
	result["document_count"] = len(docs)

	// 收集所有文档的内容
	var allContent []map[string]interface{}
	for i, doc := range docs {
		docContent := map[string]interface{}{
			"index":       i,
			"content":     doc.Content,
			"content_len": len(doc.Content),
		}

		// 复制文档元数据
		if doc.MetaData != nil {
			for k, v := range doc.MetaData {
				docContent[k] = v
			}
		}

		allContent = append(allContent, docContent)
	}

	result["documents"] = allContent

	// 添加原始选项
	if extraMeta != nil {
		result["options"] = extraMeta
	}

	return result, nil
}
