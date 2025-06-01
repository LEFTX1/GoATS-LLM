package parser

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEinoPDFTextExtractor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	extractor, err := NewEinoPDFTextExtractor(ctx)
	require.NoError(t, err, "创建PDF提取器不应返回错误")
	require.NotNil(t, extractor, "创建的PDF提取器不应为nil")
	require.NotNil(t, extractor.parser, "PDF提取器内部的parser不应为nil")
	require.NotNil(t, extractor.logger, "PDF提取器应该有默认的logger")

	// 测试带自定义logger的创建
	customLogger := log.New(os.Stdout, "[测试PDF提取器] ", log.LstdFlags)
	extractorWithCustomLogger, err := NewEinoPDFTextExtractor(ctx, WithEinoLogger(customLogger))
	require.NoError(t, err, "创建带自定义logger的PDF提取器不应返回错误")
	require.Equal(t, customLogger, extractorWithCustomLogger.logger, "应该使用提供的自定义logger")
}

func TestExtractFullTextFromPDFFile(t *testing.T) {
	// 直接使用已知存在的PDF文件
	testPDFs := []string{
		"testdata/黑白整齐简历模板 (6).pdf",
		"../testdata/黑白整齐简历模板 (6).pdf",
		"../../testdata/黑白整齐简历模板 (6).pdf",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	extractor, err := NewEinoPDFTextExtractor(ctx)
	require.NoError(t, err, "创建PDF提取器不应返回错误")

	// 尝试使用不同路径
	var filePath string
	var foundFile bool

	for _, path := range testPDFs {
		if _, err := os.Stat(path); err == nil {
			filePath = path
			foundFile = true
			break
		}
	}

	if !foundFile {
		t.Skip("找不到测试PDF文件，跳过测试")
		return
	}

	text, metadata, err := extractor.ExtractFullTextFromPDFFile(ctx, filePath)
	require.NoError(t, err, "PDF提取不应返回错误")

	// 测试文本内容
	assert.NotEmpty(t, text, "提取的文本内容不应为空")
	t.Logf("从%s提取了%d个字符的文本", filePath, len(text))

	// 测试元数据
	assert.NotNil(t, metadata, "元数据不应为nil")
	assert.Contains(t, metadata, "source_file_path", "元数据应该包含source_file_path")
	assert.Equal(t, filePath, metadata["source_file_path"], "source_file_path应该是文件路径")

	// 为了调试和可视化，打印出部分文本（前100个字符）
	if len(text) > 100 {
		t.Logf("提取的文本前100个字符: %s...", text[:100])
	} else {
		t.Logf("提取的全部文本: %s", text)
	}
}

func TestExtractTextFromReader(t *testing.T) {
	// 直接使用已知存在的PDF文件
	testPDFs := []string{
		"testdata/黑白整齐简历模板 (6).pdf",
		"../testdata/黑白整齐简历模板 (6).pdf",
		"../../testdata/黑白整齐简历模板 (6).pdf",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	extractor, err := NewEinoPDFTextExtractor(ctx)
	require.NoError(t, err, "创建PDF提取器不应返回错误")

	// 尝试使用不同路径
	var filePath string
	var foundFile bool

	for _, path := range testPDFs {
		if _, err := os.Stat(path); err == nil {
			filePath = path
			foundFile = true
			break
		}
	}

	if !foundFile {
		t.Skip("找不到测试PDF文件，跳过测试")
		return
	}

	// 打开文件供Reader测试使用
	file, err := os.Open(filePath)
	require.NoError(t, err, "打开测试PDF文件不应返回错误")
	defer file.Close()

	// 测试Reader方法
	text, metadata, err := extractor.ExtractTextFromReader(ctx, file, "test_uri", map[string]interface{}{
		"test_meta_key": "test_meta_value",
	})
	require.NoError(t, err, "从Reader提取文本不应返回错误")

	// 测试文本内容
	assert.NotEmpty(t, text, "提取的文本内容不应为空")

	// 测试元数据
	assert.NotNil(t, metadata, "元数据不应为nil")
	assert.Contains(t, metadata, "test_meta_key", "元数据应包含我们传入的键")

	// 为了调试和可视化，打印出部分文本
	if len(text) > 100 {
		t.Logf("提取的文本前100个字符: %s...", text[:100])
	} else {
		t.Logf("提取的全部文本: %s", text)
	}
}

// TestExtractTextFromMockPDF 使用模拟PDF数据测试文本提取
func TestExtractTextFromMockPDF(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	extractor, err := NewEinoPDFTextExtractor(ctx)
	require.NoError(t, err, "创建PDF提取器不应返回错误")

	// 创建一个模拟的PDF内容（不是真正的PDF，但用于测试流程）
	mockPDFContent := []byte("%PDF-1.5\nMock PDF content for testing\nThis is not a real PDF file\n")
	mockReader := bytes.NewReader(mockPDFContent)

	// 这个测试预期会失败，但我们关注的是流程和错误处理
	text, metadata, err := extractor.ExtractTextFromReader(ctx, mockReader, "mock_pdf.pdf", map[string]interface{}{
		"mock_test": true,
		"test_id":   "mock_test_001",
	})

	// 由于这不是有效的PDF，我们预期会有错误
	if err == nil {
		t.Log("注意：模拟PDF解析成功，这可能表明解析器太宽松")
	} else {
		t.Logf("预期的错误: %v", err)
	}

	// 即使失败，我们也检查元数据是否按预期处理
	if metadata != nil {
		assert.Equal(t, true, metadata["mock_test"], "元数据应包含我们传入的值")
		assert.Equal(t, "mock_test_001", metadata["test_id"], "元数据应包含我们传入的值")
	}

	// 记录任何提取到的文本
	if text != "" {
		t.Logf("从模拟PDF提取的文本: %s", text)
	}
}

// TestExtractFromEmptyFile 测试从空文件提取文本
func TestExtractFromEmptyFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	extractor, err := NewEinoPDFTextExtractor(ctx)
	require.NoError(t, err, "创建PDF提取器不应返回错误")

	// 创建临时空文件
	tempFile, err := os.CreateTemp("", "empty-*.pdf")
	require.NoError(t, err, "创建临时文件不应返回错误")
	tempFilePath := tempFile.Name()
	tempFile.Close()
	defer os.Remove(tempFilePath) // 测试结束后删除临时文件

	// 测试处理空文件
	text, metadata, err := extractor.ExtractFullTextFromPDFFile(ctx, tempFilePath)

	// 记录结果，无论是否有错误
	t.Logf("从空文件提取结果: 文本长度=%d, 错误=%v", len(text), err)
	if metadata != nil {
		t.Logf("元数据包含 %d 项", len(metadata))
	}

	// 这里我们不断言特定的错误，因为不同的PDF解析库可能有不同的行为
	// 但我们应该确保它不会崩溃并给出某种形式的反馈
}

// TestExtractFromNonExistentFile 测试从不存在的文件提取文本
func TestExtractFromNonExistentFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	extractor, err := NewEinoPDFTextExtractor(ctx)
	require.NoError(t, err, "创建PDF提取器不应返回错误")

	// 使用一个不太可能存在的文件路径
	nonExistentPath := "/path/to/non/existent/file-" + time.Now().Format("20060102150405") + ".pdf"

	// 测试处理不存在的文件
	_, _, err = extractor.ExtractFullTextFromPDFFile(ctx, nonExistentPath)

	// 断言应该有错误
	require.Error(t, err, "从不存在的文件提取应该返回错误")
	assert.Contains(t, err.Error(), "failed to open PDF file", "错误消息应该指示文件打开失败")
}

// findTestPDFFiles 查找测试目录中的PDF文件
func findTestPDFFiles() []string {
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

// TestPDFExtractAndPrint 测试提取PDF并打印内容片段，用于查看提取效果
func TestPDFExtractAndPrint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	extractor, err := NewEinoPDFTextExtractor(ctx)
	require.NoError(t, err, "创建PDF提取器不应返回错误")

	// 首先尝试直接使用用户提供的PDF文件
	testSpecificFile := "testdata/黑白整齐简历模板 (6).pdf"
	// 搜索可能的路径
	specificPaths := []string{
		testSpecificFile,
		"../testdata/黑白整齐简历模板 (6).pdf",
		"../../testdata/黑白整齐简历模板 (6).pdf",
	}

	var foundUserPDF string
	for _, path := range specificPaths {
		if _, err := os.Stat(path); err == nil {
			foundUserPDF = path
			break
		}
	}

	// 如果找到了用户指定的文件，就打印详细信息
	if foundUserPDF != "" {
		t.Logf("发现用户提供的PDF文件: %s", foundUserPDF)
		text, metadata, err := extractor.ExtractFullTextFromPDFFile(ctx, foundUserPDF)
		require.NoError(t, err, "从PDF提取文本不应返回错误")

		// 显示文本摘要
		t.Logf("【提取结果】文件路径: %s", foundUserPDF)
		t.Logf("【提取结果】文本总长度: %d 字符", len(text))

		// 一次性打印完整PDF内容，用于检查乱码问题
		t.Logf("\n========== PDF完整文本内容（用于检查乱码）==========\n%s\n========== PDF文本内容结束 ==========", text)

		// 打印乱码检测结果
		invalidChars := findInvalidCharacters(text)
		if len(invalidChars) > 0 {
			t.Logf("\n【乱码检测】发现可能的乱码字符: %s", invalidChars)
		} else {
			t.Logf("\n【乱码检测】未发现明显乱码字符")
		}

		// 打印元数据
		t.Logf("\n【提取结果】元数据 (%d项):", len(metadata))
		for k, v := range metadata {
			t.Logf("  %s: %v", k, v)
		}

		// 打印简单的文本分析
		lines := strings.Split(text, "\n")
		t.Logf("【提取结果】行数: %d", len(lines))
		paragraphs := strings.Split(strings.ReplaceAll(text, "\n\n", "§"), "§")
		t.Logf("【提取结果】段落数: %d", len(paragraphs))

		return
	}

	// 如果找不到特定文件，继续使用查找所有PDF文件的逻辑
	testFiles := findTestPDFFiles()
	if len(testFiles) == 0 {
		t.Skip("找不到任何测试PDF文件，跳过测试")
		return
	}

	t.Logf("找到 %d 个测试PDF文件", len(testFiles))
	for _, pdfPath := range testFiles {
		t.Run(filepath.Base(pdfPath), func(t *testing.T) {
			text, metadata, err := extractor.ExtractFullTextFromPDFFile(ctx, pdfPath)
			require.NoError(t, err, "从PDF提取文本不应返回错误")

			// 显示文本摘要
			t.Logf("PDF文件: %s", pdfPath)
			t.Logf("提取文本长度: %d 字符", len(text))

			// 完整打印文本内容
			t.Logf("\n========== 完整文本内容 ==========\n%s\n========== 内容结束 ==========", text)

			// 打印元数据
			t.Logf("元数据包含 %d 项:", len(metadata))
			for k, v := range metadata {
				t.Logf("  %s: %v", k, v)
			}
		})
	}
}

// findInvalidCharacters 检测可能的乱码字符
func findInvalidCharacters(text string) string {
	var invalidChars strings.Builder
	seen := make(map[rune]bool)

	for _, r := range text {
		// 检查是否为可打印的ASCII字符、常见标点符号或中文字符
		if r > 127 && // 非ASCII字符
			(r < 0x4E00 || r > 0x9FFF) && // 不是常用汉字范围
			!seen[r] && // 之前未见过此字符
			!isCommonSymbol(r) { // 不是常见符号
			invalidChars.WriteRune(r)
			seen[r] = true
		}
	}

	return invalidChars.String()
}

// isCommonSymbol 检查是否为常见符号
func isCommonSymbol(r rune) bool {
	// 中文标点符号范围
	if (r >= 0x3000 && r <= 0x303F) || // CJK标点符号
		(r >= 0xFF00 && r <= 0xFFEF) { // 全角ASCII、全角标点
		return true
	}

	// 一些常见符号列表（简化版，避免非法rune问题）
	commonSymbols := []rune{
		'·', '…', '—', '–', '©', '®', '™', '°', '±', '×', '÷',
		'≈', '≠', '≤', '≥', '∞', '√', '¥', '$', '€', '£', '¢',
		'←', '↑', '→', '↓', '★', '☆',
	}

	for _, symbol := range commonSymbols {
		if r == symbol {
			return true
		}
	}

	return false
}
