package parser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

// PDFExtractor PDF提取器接口 - 与processor包中定义相同
type PDFExtractor interface {
	// ExtractFromFile 从PDF文件提取文本和元数据
	ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error)

	// ExtractTextFromReader 从io.Reader提取文本和元数据
	ExtractTextFromReader(ctx context.Context, reader io.Reader, uri string, options interface{}) (string, map[string]interface{}, error)

	// ExtractTextFromBytes 从字节数组提取文本和元数据
	ExtractTextFromBytes(ctx context.Context, data []byte, uri string, options interface{}) (string, map[string]interface{}, error)

	// ExtractStructuredContent 提取结构化内容（如段落、表格等）
	ExtractStructuredContent(ctx context.Context, reader io.Reader, uri string, options interface{}) (map[string]interface{}, error)
}

// TikaPDFExtractor 是基于Apache Tika的PDF解析器
type TikaPDFExtractor struct {
	// Tika服务器地址，例如 http://localhost:9998
	ServerURL string
	// HTTP客户端，可配置超时等参数
	Client *http.Client
	// 是否提取完整元数据
	extractFullMetadata bool
	// 是否提取精简元数据
	extractMinimalMetadata bool
	// 是否提取链接注释文本
	extractAnnotations bool
	// 日志记录
	logger *log.Logger
}

// TikaOption 定义配置选项函数
type TikaOption func(*TikaPDFExtractor)

// WithFullMetadata 配置是否提取完整元数据
func WithFullMetadata(extract bool) TikaOption {
	return func(e *TikaPDFExtractor) {
		e.extractFullMetadata = extract
	}
}

// WithMinimalMetadata 配置是否提取精简的关键元数据
func WithMinimalMetadata(extract bool) TikaOption {
	return func(e *TikaPDFExtractor) {
		e.extractMinimalMetadata = extract
	}
}

// WithAnnotations 配置是否提取PDF链接注释文本
func WithAnnotations(extract bool) TikaOption {
	return func(e *TikaPDFExtractor) {
		e.extractAnnotations = extract
	}
}

// WithTikaLogger 配置自定义日志记录器
func WithTikaLogger(logger *log.Logger) TikaOption {
	return func(e *TikaPDFExtractor) {
		e.logger = logger
	}
}

// WithTimeout 配置HTTP客户端超时时间
func WithTimeout(timeout time.Duration) TikaOption {
	return func(e *TikaPDFExtractor) {
		e.Client.Timeout = timeout
	}
}

// 确保TikaPDFExtractor实现了PDFExtractor接口
var _ PDFExtractor = (*TikaPDFExtractor)(nil)

// NewTikaPDFExtractor 创建一个新的Tika PDF解析器
func NewTikaPDFExtractor(serverURL string, options ...TikaOption) PDFExtractor {
	// 设置默认的HTTP客户端，包含合理的超时设置
	client := &http.Client{
		Timeout: 60 * time.Second, // 设置60秒超时
	}

	extractor := &TikaPDFExtractor{
		ServerURL:              serverURL,
		Client:                 client,
		extractFullMetadata:    false, // 默认不提取完整元数据
		extractMinimalMetadata: true,  // 默认提取精简元数据
		extractAnnotations:     true,  // 默认提取注释文本
		logger:                 log.New(os.Stderr, "[TikaPDF] ", log.LstdFlags),
	}

	// 应用选项
	for _, option := range options {
		option(extractor)
	}

	return extractor
}

// ExtractTextFromReader 从io.Reader提取文本内容
func (e *TikaPDFExtractor) ExtractTextFromReader(ctx context.Context, reader io.Reader, uri string, options interface{}) (string, map[string]interface{}, error) {
	startTime := time.Now()
	e.logger.Printf("开始从Reader提取PDF文本 (URI: %s)", uri)

	// 读取所有内容到内存
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", nil, fmt.Errorf("读取PDF内容失败: %w", err)
	}

	// 提取文本和元数据
	text, metadata, err := e.ExtractTextFromBytes(ctx, data, uri, options)

	duration := time.Since(startTime)
	if err != nil {
		e.logger.Printf("从Reader提取PDF失败: %s (用时 %.2f秒)", err, duration.Seconds())
		return "", nil, err
	}

	e.logger.Printf("PDF文本提取完成: 提取了 %d 个字符 (用时 %.2f秒)", len(text), duration.Seconds())
	return text, metadata, nil
}

// ExtractTextFromBytes 从字节数组提取文本内容
func (e *TikaPDFExtractor) ExtractTextFromBytes(ctx context.Context, data []byte, uri string, options interface{}) (string, map[string]interface{}, error) {
	startTime := time.Now()

	// 基本元数据，无论如何都会包含
	baseMetadata := map[string]interface{}{
		"extraction_time":  time.Now().Format(time.RFC3339),
		"source_file_path": uri,
	}

	// 构建请求URL - 纯文本模式
	url := fmt.Sprintf("%s/tika", e.ServerURL)

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return "", baseMetadata, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 设置头信息
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Accept", "text/plain")

	// 如果有URI，添加到请求头
	if uri != "" {
		req.Header.Set("X-Tika-Resource-Name", uri)
	}

	// 根据配置决定是否提取注释文本
	if !e.extractAnnotations {
		req.Header.Set("X-Tika-PDFExtractAnnotationText", "false")
	}

	// 发送请求
	resp, err := e.Client.Do(req)
	if err != nil {
		return "", baseMetadata, fmt.Errorf("发送请求到Tika服务器失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		return "", baseMetadata, fmt.Errorf("tika服务器返回错误状态码: %d", resp.StatusCode)
	}

	// 读取响应内容
	textBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", baseMetadata, fmt.Errorf("读取Tika响应失败: %w", err)
	}

	text := string(textBytes)

	// 添加文本长度到基本元数据
	baseMetadata["text_length"] = len(text)
	baseMetadata["processing_duration_ms"] = time.Since(startTime).Milliseconds()

	// 如果不需要任何元数据，直接返回基本元数据
	if !e.extractMinimalMetadata && !e.extractFullMetadata {
		return text, baseMetadata, nil
	}

	// 提取元数据
	if e.extractFullMetadata || e.extractMinimalMetadata {
		metadataStartTime := time.Now()
		rawMetadata, err := e.extractMetadata(ctx, data, uri)

		if err == nil {
			if e.extractFullMetadata {
				// 合并所有元数据
				for k, v := range rawMetadata {
					baseMetadata[k] = v
				}
			} else if e.extractMinimalMetadata {
				// 只添加重要的元数据
				for k, v := range rawMetadata {
					if isImportantMetadata(k) {
						baseMetadata[k] = v
					}
				}
			}
		} else {
			e.logger.Printf("元数据提取失败: %v, 继续使用基本元数据", err)
		}

		baseMetadata["metadata_processing_ms"] = time.Since(metadataStartTime).Milliseconds()
	}

	return text, baseMetadata, nil
}

// 判断元数据字段是否重要
func isImportantMetadata(key string) bool {
	importantKeys := map[string]bool{
		"pdf:PDFVersion":                true,
		"xmpTPg:NPages":                 true,
		"dcterms:created":               true,
		"language":                      true,
		"pdf:charsPerPage":              true,
		"dc:title":                      true,
		"Content-Type":                  true,
		"pdf:docinfo:title":             true,
		"pdf:docinfo:created":           true,
		"pdf:totalUnmappedUnicodeChars": true,
	}
	return importantKeys[key]
}

// ExtractStructuredContent 提取结构化内容
func (e *TikaPDFExtractor) ExtractStructuredContent(ctx context.Context, reader io.Reader, uri string, options interface{}) (map[string]interface{}, error) {
	startTime := time.Now()
	e.logger.Printf("开始从Reader提取结构化内容 (URI: %s)", uri)

	// 读取所有内容到内存
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("读取PDF内容失败: %w", err)
	}

	// 构建请求URL - XHTML模式，提供结构化内容
	url := fmt.Sprintf("%s/tika", e.ServerURL)

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 设置头信息
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Accept", "text/html") // 或者 "application/xhtml+xml"

	// 如果有URI，添加到请求头
	if uri != "" {
		req.Header.Set("X-Tika-Resource-Name", uri)
	}

	// 发送请求
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求到Tika服务器失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tika服务器返回错误状态码: %d", resp.StatusCode)
	}

	// 读取响应内容（HTML格式）
	htmlBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取Tika响应失败: %w", err)
	}

	// 构建基本结果
	result := map[string]interface{}{
		"html":                   string(htmlBytes),
		"extraction_time":        time.Now().Format(time.RFC3339),
		"processing_duration_ms": time.Since(startTime).Milliseconds(),
	}

	// 根据配置决定是否添加元数据
	if e.extractFullMetadata || e.extractMinimalMetadata {
		metadataStartTime := time.Now()
		metadata, err := e.extractMetadata(ctx, data, uri)

		if err == nil {
			if e.extractFullMetadata {
				result["metadata"] = metadata
			} else if e.extractMinimalMetadata {
				// 只添加重要的元数据
				importantMetadata := make(map[string]interface{})
				for k, v := range metadata {
					if isImportantMetadata(k) {
						importantMetadata[k] = v
					}
				}
				result["metadata"] = importantMetadata
			}
		} else {
			e.logger.Printf("元数据提取失败: %v", err)
			result["metadata"] = map[string]interface{}{}
		}

		result["metadata_processing_ms"] = time.Since(metadataStartTime).Milliseconds()
	}

	return result, nil
}

// extractMetadata 提取文档元数据
func (e *TikaPDFExtractor) extractMetadata(ctx context.Context, data []byte, uri string) (map[string]interface{}, error) {
	// 构建请求URL - 元数据
	url := fmt.Sprintf("%s/meta", e.ServerURL)

	// 创建请求
	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 设置头信息
	req.Header.Set("Content-Type", "application/pdf")
	req.Header.Set("Accept", "application/json")

	// 如果有URI，添加到请求头
	if uri != "" {
		req.Header.Set("X-Tika-Resource-Name", uri)
	}

	// 发送请求
	resp, err := e.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送请求到Tika服务器失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tika服务器返回错误状态码: %d", resp.StatusCode)
	}

	// 读取响应内容
	metadataBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取Tika响应失败: %w", err)
	}

	// 解析JSON元数据
	var metadata map[string]interface{}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		return nil, fmt.Errorf("解析元数据JSON失败: %w", err)
	}

	return metadata, nil
}

// ExtractTables 提取PDF中的表格数据
func (e *TikaPDFExtractor) ExtractTables(ctx context.Context, reader io.Reader, uri string) ([]map[string]interface{}, error) {
	// 提取结构化内容
	_, err := e.ExtractStructuredContent(ctx, reader, uri, nil)
	if err != nil {
		return nil, err
	}

	// 这里需要根据返回的HTML内容解析表格结构
	// 这通常涉及到HTML解析和表格提取的逻辑
	// 简化起见，这里仅返回一个空数组
	// 实际实现需要使用HTML解析库（如goquery）来提取表格

	// TODO: 实现HTML表格解析逻辑

	return []map[string]interface{}{}, nil
}

// ExtractFromFile 从PDF文件提取文本内容
func (e *TikaPDFExtractor) ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error) {
	startTime := time.Now()
	e.logger.Printf("开始处理PDF文件: %s", filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return "", nil, fmt.Errorf("打开PDF文件 %s 失败: %w", filePath, err)
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
