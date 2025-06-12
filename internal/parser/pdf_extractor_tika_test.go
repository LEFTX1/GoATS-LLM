package parser

import (
	"ai-agent-go/internal/config"
	"bytes"
	"context"
	"io"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTikaPDFExtractor(t *testing.T) {
	// 加载配置
	cfg, err := config.LoadConfigFromFileAndEnv("../../internal/config/config.yaml")
	if err != nil {
		t.Logf("无法加载配置文件: %v，使用默认URL", err)
		cfg = &config.Config{}
		cfg.Tika.ServerURL = "http://localhost:9998"
	}

	// 测试创建新的Tika PDF解析器（默认选项）
	extractorInterface := NewTikaPDFExtractor(cfg.Tika.ServerURL)
	extractor, ok := extractorInterface.(*TikaPDFExtractor)
	require.True(t, ok, "应该能够将接口转换为TikaPDFExtractor类型")

	require.NotNil(t, extractor, "创建的Tika PDF提取器不应为nil")
	assert.Equal(t, cfg.Tika.ServerURL, extractor.ServerURL, "ServerURL应该被正确设置")
	require.NotNil(t, extractor.Client, "HTTP客户端不应为nil")
	assert.Equal(t, 60*time.Second, extractor.Client.Timeout, "HTTP客户端超时应为60秒")
	assert.False(t, extractor.extractFullMetadata, "默认应该不提取完整元数据")
	assert.True(t, extractor.extractMinimalMetadata, "默认应该提取精简元数据")

	// 测试创建带自定义选项的解析器
	customLogger := log.New(os.Stdout, "[测试] ", log.LstdFlags)
	customExtractorInterface := NewTikaPDFExtractor(
		cfg.Tika.ServerURL,
		WithFullMetadata(true),
		WithMinimalMetadata(false),
		WithTikaLogger(customLogger),
		WithTimeout(30*time.Second),
	)

	customExtractor, ok := customExtractorInterface.(*TikaPDFExtractor)
	require.True(t, ok, "应该能够将接口转换为TikaPDFExtractor类型")

	assert.Equal(t, true, customExtractor.extractFullMetadata, "应该设置为提取完整元数据")
	assert.Equal(t, false, customExtractor.extractMinimalMetadata, "应该设置为不提取精简元数据")
	assert.Equal(t, customLogger, customExtractor.logger, "应该使用提供的自定义logger")
	assert.Equal(t, 30*time.Second, customExtractor.Client.Timeout, "应该使用自定义超时")
}

// 创建一个模拟的Tika服务器，用于测试
func createMockTikaServer() *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tika":
			// 模拟Tika提取文本
			if r.Header.Get("Accept") == "text/plain" {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("这是从PDF中提取的测试文本内容。"))
			} else if r.Header.Get("Accept") == "text/html" {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("<html><body>这是HTML格式的测试内容</body></html>"))
			}
		case "/meta":
			// 模拟Tika提取元数据
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"Content-Type": "application/pdf",
				"pdf:PDFVersion": "1.5",
				"pdf:pageCount": 2,
				"meta:author": "测试作者",
				"dc:title": "测试文档",
				"language": "zh-cn",
				"pdf:charsPerPage": 500,
				"dcterms:created": "2025-01-01T00:00:00Z",
				"access_permission:can_print": true,
				"X-TIKA:Parsed-By": "org.apache.tika.parser.DefaultParser",
				"xmpTPg:NPages": 2
			}`))
		default:
			// 未知路径返回404
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	return server
}

func TestMetadataOptions(t *testing.T) {
	// 创建模拟服务器
	server := createMockTikaServer()
	defer server.Close()

	ctx := context.Background()
	mockPDFContent := []byte("%PDF-1.5\nMock PDF content for testing\nThis is not a real PDF file\n")

	// 测试1：不提取任何元数据
	noMetaExtractorInterface := NewTikaPDFExtractor(server.URL, WithMinimalMetadata(false), WithFullMetadata(false))
	noMetaExtractor, ok := noMetaExtractorInterface.(*TikaPDFExtractor)
	require.True(t, ok, "应该能够将接口转换为TikaPDFExtractor类型")

	text1, meta1, err := noMetaExtractor.ExtractTextFromBytes(ctx, mockPDFContent, "test.pdf", nil)
	require.NoError(t, err)
	assert.Contains(t, text1, "这是从PDF中提取的测试文本内容")
	assert.Contains(t, meta1, "extraction_time")
	assert.Contains(t, meta1, "processing_duration_ms")
	assert.NotContains(t, meta1, "pdf:PDFVersion") // 不应包含PDF元数据
	t.Logf("无元数据模式返回了 %d 个元数据项", len(meta1))

	// 测试2：提取精简元数据（使用选项显式配置）
	minMetaExtractorInterface := NewTikaPDFExtractor(server.URL, WithMinimalMetadata(true))
	minMetaExtractor, ok := minMetaExtractorInterface.(*TikaPDFExtractor)
	require.True(t, ok, "应该能够将接口转换为TikaPDFExtractor类型")

	_, meta2, err := minMetaExtractor.ExtractTextFromBytes(ctx, mockPDFContent, "test.pdf", nil)
	require.NoError(t, err)
	assert.Contains(t, meta2, "pdf:PDFVersion") // 应包含重要元数据
	assert.Contains(t, meta2, "xmpTPg:NPages")
	assert.NotContains(t, meta2, "X-TIKA:Parsed-By") // 不应包含不重要元数据
	t.Logf("精简元数据模式返回了 %d 个元数据项", len(meta2))

	// 测试3：提取完整元数据
	fullMetaExtractorInterface := NewTikaPDFExtractor(server.URL, WithFullMetadata(true))
	fullMetaExtractor, ok := fullMetaExtractorInterface.(*TikaPDFExtractor)
	require.True(t, ok, "应该能够将接口转换为TikaPDFExtractor类型")

	_, meta3, err := fullMetaExtractor.ExtractTextFromBytes(ctx, mockPDFContent, "test.pdf", nil)
	require.NoError(t, err)
	assert.Contains(t, meta3, "pdf:PDFVersion")
	assert.Contains(t, meta3, "X-TIKA:Parsed-By") // 应包含全部元数据
	assert.Contains(t, meta3, "meta:author")
	t.Logf("完整元数据模式返回了 %d 个元数据项", len(meta3))

	// 输出不同模式的处理时间
	if meta1["processing_duration_ms"] != nil && meta2["processing_duration_ms"] != nil && meta3["processing_duration_ms"] != nil {
		t.Logf("处理时间 - 无元数据: %v ms, 精简元数据: %v ms, 完整元数据: %v ms",
			meta1["processing_duration_ms"], meta2["processing_duration_ms"], meta3["processing_duration_ms"])
	}
}

func TestTikaExtractTextFromReader(t *testing.T) {
	// 创建模拟服务器
	server := createMockTikaServer()
	defer server.Close()

	// 创建使用模拟服务器的提取器，显式配置使用精简元数据
	extractor := NewTikaPDFExtractor(server.URL, WithMinimalMetadata(true))

	ctx := context.Background()

	// 创建一个模拟的PDF内容
	mockPDFContent := []byte("%PDF-1.5\nMock PDF content for testing\nThis is not a real PDF file\n")
	mockReader := bytes.NewReader(mockPDFContent)

	// 测试从reader提取文本
	text, metadata, err := extractor.ExtractTextFromReader(ctx, mockReader, "test_file.pdf", nil)
	require.NoError(t, err, "从Reader提取文本不应返回错误")

	// 验证提取的文本
	assert.NotEmpty(t, text, "提取的文本不应为空")
	assert.Contains(t, text, "这是从PDF中提取的测试文本内容", "应该包含模拟服务器返回的文本")

	// 验证元数据 (精简模式下应该包含重要元数据)
	assert.NotNil(t, metadata, "元数据不应为nil")
	assert.Contains(t, metadata, "Content-Type", "元数据应该包含Content-Type")
	assert.Contains(t, metadata, "xmpTPg:NPages", "元数据应该包含页数")
	assert.Equal(t, float64(2), metadata["xmpTPg:NPages"], "PDF页数应该是2")
}

func TestExtractTextFromBytes(t *testing.T) {
	// 创建模拟服务器
	server := createMockTikaServer()
	defer server.Close()

	// 创建使用模拟服务器的提取器，显式配置使用精简元数据
	extractor := NewTikaPDFExtractor(server.URL, WithMinimalMetadata(true))

	ctx := context.Background()

	// 创建一个模拟的PDF内容
	mockPDFContent := []byte("%PDF-1.5\nMock PDF content for testing\nThis is not a real PDF file\n")

	// 测试从字节数组提取文本
	text, metadata, err := extractor.ExtractTextFromBytes(ctx, mockPDFContent, "test_file.pdf", nil)
	require.NoError(t, err, "从字节数组提取文本不应返回错误")

	// 验证提取的文本
	assert.NotEmpty(t, text, "提取的文本不应为空")
	assert.Contains(t, text, "这是从PDF中提取的测试文本内容", "应该包含模拟服务器返回的文本")

	// 验证元数据
	assert.NotNil(t, metadata, "元数据不应为nil")
	assert.Contains(t, metadata, "dc:title", "元数据应该包含标题")
	assert.Equal(t, "测试文档", metadata["dc:title"], "文档标题应该正确")
}

func TestExtractStructuredContent(t *testing.T) {
	// 创建模拟服务器
	server := createMockTikaServer()
	defer server.Close()

	// 创建使用模拟服务器的提取器，显式配置使用精简元数据
	extractor := NewTikaPDFExtractor(server.URL, WithMinimalMetadata(true))

	ctx := context.Background()

	// 创建一个模拟的PDF内容
	mockPDFContent := []byte("%PDF-1.5\nMock PDF content for testing\nThis is not a real PDF file\n")
	mockReader := bytes.NewReader(mockPDFContent)

	// 测试提取结构化内容
	content, err := extractor.ExtractStructuredContent(ctx, mockReader, "test_file.pdf", nil)
	require.NoError(t, err, "提取结构化内容不应返回错误")

	// 验证提取的内容
	assert.NotNil(t, content, "结构化内容不应为nil")
	assert.Contains(t, content, "html", "结构化内容应该包含HTML部分")
	assert.Contains(t, content, "metadata", "结构化内容应该包含元数据部分")
	assert.Contains(t, content, "extraction_time", "结构化内容应该包含提取时间")
	assert.Contains(t, content, "processing_duration_ms", "结构化内容应该包含处理时间")

	// 验证HTML内容
	htmlContent, ok := content["html"].(string)
	assert.True(t, ok, "html内容应该是字符串类型")
	assert.Contains(t, htmlContent, "这是HTML格式的测试内容", "HTML内容应该正确")

	// 验证元数据
	metadata, ok := content["metadata"].(map[string]interface{})
	assert.True(t, ok, "metadata应该是map类型")
	assert.Contains(t, metadata, "dc:title", "元数据应该包含标题")
	assert.Equal(t, "测试文档", metadata["dc:title"], "文档标题应该正确")
}

func TestServerError(t *testing.T) {
	// 创建一个总是返回错误的服务器
	errorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("Internal Server Error"))
	}))
	defer errorServer.Close()

	// 创建使用错误服务器的提取器
	extractor := NewTikaPDFExtractor(errorServer.URL)

	ctx := context.Background()

	// 测试处理服务器错误
	mockPDFContent := []byte("%PDF-1.5\nMock content\n")
	_, _, err := extractor.ExtractTextFromBytes(ctx, mockPDFContent, "test_file.pdf", nil)

	// 应该返回错误
	require.Error(t, err, "服务器错误应该导致提取失败")
	assert.Contains(t, err.Error(), "Tika服务器返回错误状态码", "错误消息应该指示服务器错误")
}

func TestConnectionError(t *testing.T) {
	// 使用一个不存在的服务器地址
	extractor := NewTikaPDFExtractor("http://localhost:99999")

	ctx := context.Background()

	// 测试处理连接错误
	mockPDFContent := []byte("%PDF-1.5\nMock content\n")
	_, _, err := extractor.ExtractTextFromBytes(ctx, mockPDFContent, "test_file.pdf", nil)

	// 应该返回错误
	require.Error(t, err, "连接错误应该导致提取失败")
	assert.Contains(t, err.Error(), "发送请求到Tika服务器失败", "错误消息应该指示连接问题")
}

// TestTikaUTF8HeaderIsSet 验证对Tika的请求是否包含正确的UTF-8编码头
func TestTikaUTF8HeaderIsSet(t *testing.T) {
	expectedText := "你好，世界"
	var requestHeaders http.Header

	// 1. 创建一个模拟Tika服务器，它会检查请求头
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 记录请求头以供后续验证
		requestHeaders = r.Header

		// 检查请求路径
		if r.URL.Path != "/tika" {
			http.NotFound(w, r)
			return
		}

		// 检查请求方法
		if r.Method != http.MethodPut {
			http.Error(w, "Expected PUT request", http.StatusMethodNotAllowed)
			return
		}

		// 验证Accept头是否明确要求UTF-8
		acceptHeader := r.Header.Get("Accept")
		if !strings.Contains(acceptHeader, "charset=utf-8") {
			http.Error(w, "Accept header missing 'charset=utf-8'", http.StatusBadRequest)
			return
		}

		// 返回一个包含中文字符的UTF-8编码响应
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(expectedText))
	}))
	defer mockServer.Close()

	// 2. 创建一个指向模拟服务器的Tika提取器，并禁用元数据提取，以确保只测试文本提取的请求
	extractor := NewTikaPDFExtractor(mockServer.URL, WithMinimalMetadata(false))

	// 3. 准备调用提取器的参数
	ctx := context.Background()
	mockPDFContent := []byte("%PDF-1.5\nMock PDF\n")

	// 4. 调用文本提取方法
	actualText, _, err := extractor.ExtractTextFromBytes(ctx, mockPDFContent, "test-utf8.pdf", nil)
	require.NoError(t, err, "从模拟服务器提取UTF-8文本不应返回错误")

	// 5. 验证结果
	// 验证返回的文本是否正确
	assert.Equal(t, expectedText, actualText, "提取的文本应与模拟服务器返回的UTF-8字符串完全匹配")

	// 验证请求头是否被正确设置
	require.NotNil(t, requestHeaders, "请求头不应为nil")
	assert.Equal(t, "text/plain; charset=utf-8", requestHeaders.Get("Accept"), "Accept头应该包含charset=utf-8")
	assert.Equal(t, "utf-8", requestHeaders.Get("Accept-Charset"), "Accept-Charset头应该是utf-8")

	t.Log("测试成功：请求Tika时正确设置了UTF-8相关的Accept和Accept-Charset头")
}

// 集成测试 - 如果有真实的Tika服务器可用，则运行
func TestWithRealTikaServer(t *testing.T) {
	// 加载配置
	cfg, err := config.LoadConfigFromFileAndEnv("../../internal/config/config.yaml")
	if err != nil {
		t.Skip("无法加载配置文件，跳过真实Tika服务器测试")
		return
	}

	// 检查配置中是否有Tika服务器URL
	if cfg.Tika.ServerURL == "" {
		t.Skip("配置文件中没有指定Tika服务器URL，跳过真实Tika服务器测试")
		return
	}

	extractor := NewTikaPDFExtractor(cfg.Tika.ServerURL, WithMinimalMetadata(true))
	ctx := context.Background()

	// 查找测试PDF文件
	testFiles := findTikaPDFFiles()
	if len(testFiles) == 0 {
		t.Skip("找不到测试PDF文件，跳过测试")
		return
	}

	// 选择第一个PDF文件进行测试
	pdfPath := testFiles[0]
	t.Logf("使用PDF文件: %s", pdfPath)

	// 打开文件
	file, err := os.Open(pdfPath)
	require.NoError(t, err, "打开测试PDF文件不应返回错误")
	defer file.Close()

	// 读取文件内容
	pdfData, err := io.ReadAll(file)
	require.NoError(t, err, "读取PDF文件内容不应返回错误")

	// 测试从字节数组提取文本 - 精简元数据模式
	startTime := time.Now()
	text, metadata, err := extractor.ExtractTextFromBytes(ctx, pdfData, pdfPath, nil)
	defaultDuration := time.Since(startTime)

	// 这里我们不断言错误，因为真实服务器可能有各种状态
	if err != nil {
		t.Logf("从真实Tika服务器提取时出错: %v", err)
		return
	}

	// 记录结果
	t.Logf("【默认模式】提取的文本长度: %d 字符, 用时: %v", len(text), defaultDuration)
	if len(text) > 100 {
		t.Logf("提取的文本前100个字符: %s...", text[:100])
	} else {
		t.Logf("提取的全部文本: %s", text)
	}

	t.Logf("【默认模式】元数据包含 %d 项:", len(metadata))
	t.Logf("重要元数据项:")
	for k, v := range metadata {
		if isImportantMetadata(k) {
			t.Logf("  %s: %v", k, v)
		}
	}

	// 测试完整元数据模式
	fullMetaExtractor := NewTikaPDFExtractor(cfg.Tika.ServerURL, WithFullMetadata(true))
	startTime = time.Now()
	_, fullMetadata, err := fullMetaExtractor.ExtractTextFromBytes(ctx, pdfData, pdfPath, nil)
	fullMetaDuration := time.Since(startTime)

	if err == nil {
		t.Logf("【完整元数据模式】元数据包含 %d 项, 用时: %v", len(fullMetadata), fullMetaDuration)
	}

	// 测试无元数据模式
	noMetaExtractor := NewTikaPDFExtractor(cfg.Tika.ServerURL, WithMinimalMetadata(false), WithFullMetadata(false))
	startTime = time.Now()
	_, noMetadata, err := noMetaExtractor.ExtractTextFromBytes(ctx, pdfData, pdfPath, nil)
	noMetaDuration := time.Since(startTime)

	if err == nil {
		t.Logf("【无元数据模式】元数据包含 %d 项, 用时: %v", len(noMetadata), noMetaDuration)
	}

	// 比较三种模式的性能差异
	t.Logf("性能比较:")
	t.Logf("默认模式 (精简元数据): %.2f ms", float64(defaultDuration.Microseconds())/1000)
	t.Logf("完整元数据模式: %.2f ms (比默认模式慢 %.1f%%)",
		float64(fullMetaDuration.Microseconds())/1000,
		float64(fullMetaDuration-defaultDuration)/float64(defaultDuration)*100)
	t.Logf("无元数据模式: %.2f ms (比默认模式快 %.1f%%)",
		float64(noMetaDuration.Microseconds())/1000,
		float64(defaultDuration-noMetaDuration)/float64(defaultDuration)*100)
}

// 性能比较测试：Tika vs Eino
func TestCompareExtractorPerformance(t *testing.T) {
	// 加载配置
	cfg, err := config.LoadConfigFromFileAndEnv("../../internal/config/config.yaml")
	if err != nil {
		t.Skip("无法加载配置文件，跳过性能对比测试")
		return
	}

	// 检查配置中是否有Tika服务器URL
	if cfg.Tika.ServerURL == "" {
		t.Skip("配置文件中没有指定Tika服务器URL，跳过性能对比测试")
		return
	}

	// 查找测试PDF文件
	testFiles := findTikaPDFFiles()
	if len(testFiles) == 0 {
		t.Skip("找不到测试PDF文件，跳过性能对比测试")
		return
	}

	// 选择第一个PDF文件进行测试
	pdfPath := testFiles[0]
	pdfData, err := os.ReadFile(pdfPath)
	require.NoError(t, err, "读取PDF文件失败")

	ctx := context.Background()

	// 创建解析器
	tikaExtractor := NewTikaPDFExtractor(cfg.Tika.ServerURL, WithMinimalMetadata(false))
	einoExtractor, err := NewEinoPDFTextExtractor(ctx)
	if err != nil {
		t.Skip("创建Eino解析器失败，跳过性能对比测试")
		return
	}

	t.Logf("===== PDF解析器性能对比测试 =====")
	t.Logf("测试文件: %s (大小: %.2f KB)", filepath.Base(pdfPath), float64(len(pdfData))/1024)

	// Tika解析测试
	tikaStart := time.Now()
	tikaText, _, err := tikaExtractor.ExtractTextFromBytes(ctx, pdfData, pdfPath, nil)
	tikaDuration := time.Since(tikaStart)
	if err != nil {
		t.Logf("Tika解析失败: %v", err)
	} else {
		t.Logf("Tika: 耗时 %.2f ms, 提取 %d 字符", float64(tikaDuration.Microseconds())/1000, len(tikaText))
	}

	// Eino解析测试
	einoStart := time.Now()
	einoText, _, err := einoExtractor.ExtractTextFromBytes(ctx, pdfData, pdfPath, nil)
	einoDuration := time.Since(einoStart)
	if err != nil {
		t.Logf("Eino解析失败: %v", err)
	} else {
		t.Logf("Eino: 耗时 %.2f ms, 提取 %d 字符", float64(einoDuration.Microseconds())/1000, len(einoText))
	}

	// 内容比较
	if err == nil && tikaText != "" && einoText != "" {
		// 计算文本差异
		textDiff := calculateTextDifference(tikaText, einoText)
		t.Logf("文本差异: %.2f%%", textDiff*100)

		// 内容样例比较
		t.Logf("Tika文本样例: %s", truncateString(tikaText, 10000))
		t.Logf("Eino文本样例: %s", truncateString(einoText, 10000))

		// 速度比较
		if tikaDuration > 0 && einoDuration > 0 {
			t.Logf("速度比较: Eino速度是Tika的 %.2f 倍", float64(tikaDuration)/float64(einoDuration))
		}

		// 性能建议
		if einoDuration < tikaDuration {
			t.Logf("性能建议: Eino提取器速度更快，建议用于处理大量文档")
		} else {
			t.Logf("性能建议: Tika提取器速度更快，建议用于处理大量文档")
		}

		// 质量建议
		if textDiff < 0.1 {
			t.Logf("质量建议: 两个解析器提取的内容相似度高，可根据性能选择")
		} else if len(einoText) > len(tikaText) {
			t.Logf("质量建议: Eino提取的内容更多，适合需要完整内容的场景")
		} else {
			t.Logf("质量建议: Tika提取的内容更多，适合需要完整内容的场景")
		}
	}
}

// 计算两个字符串的差异率（简化实现）
func calculateTextDifference(s1, s2 string) float64 {
	maxLen := math.Max(float64(len(s1)), float64(len(s2)))
	if maxLen == 0 {
		return 0
	}

	// 使用长度差异作为简化的相似度指标
	lenDiff := math.Abs(float64(len(s1) - len(s2)))
	return lenDiff / maxLen
}

// 截断字符串，用于显示样例
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// findTikaPDFFiles 查找测试数据目录中的PDF文件
func findTikaPDFFiles() []string {
	// 搜索可能的测试数据目录
	searchDirs := []string{
		"testdata",
		"../testdata",
		"../../testdata",
	}

	var foundFiles []string
	for _, dir := range searchDirs {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, file := range files {
			if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), ".pdf") {
				foundFiles = append(foundFiles, filepath.Join(dir, file.Name()))
			}
		}
	}

	return foundFiles
}

// 测试辅助函数：提取表格
func TestExtractTables(t *testing.T) {
	// 创建模拟服务器
	server := createMockTikaServer()
	defer server.Close()

	// 创建使用模拟服务器的提取器
	extractorInterface := NewTikaPDFExtractor(server.URL, WithMinimalMetadata(true))
	extractor, ok := extractorInterface.(*TikaPDFExtractor)
	require.True(t, ok, "应该能够将接口转换为TikaPDFExtractor类型")

	ctx := context.Background()

	// 创建一个模拟的PDF内容
	mockPDFContent := []byte("%PDF-1.5\nMock PDF with tables\n")
	mockReader := bytes.NewReader(mockPDFContent)

	// 测试提取表格
	tables, err := extractor.ExtractTables(ctx, mockReader, "test_file.pdf")
	require.NoError(t, err, "提取表格不应返回错误")

	// 表格提取功能在注释中提到是简化的，返回空数组
	assert.NotNil(t, tables, "表格数组不应为nil")
	assert.Len(t, tables, 0, "表格数组应该是空的（因为实现是简化的）")
}

// 测试ExtractFromFile接口方法
func TestExtractFromFile(t *testing.T) {
	// 加载配置
	cfg, err := config.LoadConfigFromFileAndEnv("../../internal/config/config.yaml")
	if err != nil {
		t.Skip("无法加载配置文件，跳过文件提取测试")
		return
	}

	// 检查配置中是否有Tika服务器URL
	if cfg.Tika.ServerURL == "" {
		t.Skip("配置文件中没有指定Tika服务器URL，跳过文件提取测试")
		return
	}

	// 查找测试PDF文件
	testFiles := findTikaPDFFiles()
	if len(testFiles) == 0 {
		t.Skip("找不到测试PDF文件，跳过文件提取测试")
		return
	}

	// 创建提取器
	extractor := NewTikaPDFExtractor(cfg.Tika.ServerURL, WithMinimalMetadata(true))
	ctx := context.Background()

	// 选择第一个PDF文件进行测试
	pdfPath := testFiles[0]

	// 测试ExtractFromFile方法
	text, metadata, err := extractor.ExtractFromFile(ctx, pdfPath)

	if err != nil {
		t.Fatalf("从文件提取文本失败: %v", err)
	}

	assert.NotEmpty(t, text, "提取的文本不应为空")
	assert.Contains(t, metadata, "source_file_path", "元数据应包含文件路径")
	assert.Equal(t, pdfPath, metadata["source_file_path"], "文件路径应正确")

	t.Logf("从文件提取成功: 提取了 %d 个字符", len(text))
}

// TestAnnotationExtraction 测试是否可以禁用PDF注释提取
func TestAnnotationExtraction(t *testing.T) {
	// 加载配置
	cfg, err := config.LoadConfigFromFileAndEnv("../../internal/config/config.yaml")
	if err != nil {
		t.Logf("无法加载配置文件: %v，使用默认URL", err)
		cfg = &config.Config{}
		cfg.Tika.ServerURL = "http://localhost:9998"
	}

	// 查找测试PDF文件
	testFiles := findTikaPDFFiles()
	if len(testFiles) == 0 {
		t.Skip("找不到测试PDF文件，跳过测试")
		return
	}

	// 选择第一个PDF文件进行测试
	pdfPath := testFiles[0]
	t.Logf("使用PDF文件: %s", pdfPath)

	// 创建上下文
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 读取文件内容
	pdfData, err := os.ReadFile(pdfPath)
	require.NoError(t, err, "读取PDF文件内容不应返回错误")

	// 测试1: 默认配置 (提取注释)
	defaultExtractor := NewTikaPDFExtractor(cfg.Tika.ServerURL)
	defaultText, _, err := defaultExtractor.ExtractTextFromBytes(ctx, pdfData, pdfPath, nil)
	if err != nil {
		t.Fatalf("提取PDF文本失败: %v", err)
	}

	// 测试2: 禁用注释提取
	noAnnotationExtractor := NewTikaPDFExtractor(
		cfg.Tika.ServerURL,
		WithAnnotations(false),
	)
	noAnnotationText, _, err := noAnnotationExtractor.ExtractTextFromBytes(ctx, pdfData, pdfPath, nil)
	if err != nil {
		t.Fatalf("提取PDF文本失败: %v", err)
	}

	// 比较两种提取模式的结果
	t.Logf("默认提取模式 (包含注释) 文本长度: %d 字符", len(defaultText))
	t.Logf("禁用注释提取模式文本长度: %d 字符", len(noAnnotationText))

	// 检查长度差异 - 如果PDF包含链接注释，禁用注释的文本应该更短
	if len(defaultText) <= len(noAnnotationText) {
		t.Logf("警告: 禁用注释提取后文本长度未减少，可能测试文件没有链接注释或功能未生效")
	} else {
		t.Logf("禁用注释提取后文本长度减少: %d 字符", len(defaultText)-len(noAnnotationText))

		// 检查是否有URL被重复
		urlRegex := regexp.MustCompile(`https?://\S+`)
		defaultURLs := urlRegex.FindAllString(defaultText, -1)
		noAnnotationURLs := urlRegex.FindAllString(noAnnotationText, -1)

		t.Logf("默认提取模式识别URL数量: %d", len(defaultURLs))
		t.Logf("禁用注释提取模式识别URL数量: %d", len(noAnnotationURLs))

		if len(defaultURLs) > len(noAnnotationURLs) {
			t.Logf("成功: 禁用注释提取减少了URL重复")
		}
	}

	// 输出文本末尾比较（最后100个字符）
	if len(defaultText) > 100 && len(noAnnotationText) > 100 {
		t.Logf("默认提取模式文本末尾:\n%s", defaultText[len(defaultText)-100:])
		t.Logf("禁用注释提取模式文本末尾:\n%s", noAnnotationText[len(noAnnotationText)-100:])
	}
}
