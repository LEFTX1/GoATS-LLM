package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"ai-agent-go/pkg/parser"
)

// 定义分块命令的命令行参数
var (
	chunkInputFile      = flag.String("chunk-file", "", "要分块的PDF文件路径")
	chunkFormat         = flag.String("chunk-format", "text", "输出格式，可选项：text, json")
	chunkPreserveFormat = flag.Bool("chunk-preserve-format", false, "保留原始格式（如空行、缩进等）")
	chunkUseLLM         = flag.Bool("chunk-use-llm", false, "当规则识别失败时使用LLM辅助分块（需配置LLM服务）")
	chunkCustomSections = flag.String("chunk-custom-sections", "", "自定义章节名称，以逗号分隔")
	chunkMinSize        = flag.Int("chunk-min-size", 0, "最小块大小（字符数）")
)

// 处理分块子命令
func handleChunkCommand() {
	// 默认使用全局的 pdfFilePath
	inputFile := *chunkInputFile
	if inputFile == "" {
		inputFile = *pdfFilePath
	}

	if inputFile == "" {
		fmt.Println("错误: 必须提供PDF文件路径。使用 -chunk-file 或 -pdf 参数。")
		flag.Usage()
		os.Exit(1)
	}

	// 创建上下文，添加超时
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 创建PDF提取器
	extractor, err := parser.NewEinoPDFTextExtractor(ctx)
	if err != nil {
		fmt.Printf("创建PDF提取器失败: %v\n", err)
		os.Exit(1)
	}

	// 提取文本
	fmt.Println("1. 开始从PDF提取文本...")
	startTime := time.Now()

	text, metadata, err := extractor.ExtractFullTextFromPDFFile(ctx, inputFile)
	if err != nil {
		fmt.Printf("提取PDF文本失败: %v\n", err)
		os.Exit(1)
	}

	extractTime := time.Since(startTime)
	fmt.Printf("提取完成! 耗时: %v，提取了 %d 字符文本\n", extractTime, len(text))

	// 输出元数据信息
	if len(metadata) > 0 {
		fmt.Println("PDF文件元数据:")
		for k, v := range metadata {
			fmt.Printf("  %s: %v\n", k, v)
		}
	}

	// 创建简历分块器配置
	chunkerConfig := parser.ChunkerConfig{
		UseLLMFallback: *chunkUseLLM,
		PreserveFormat: *chunkPreserveFormat,
		MinChunkSize:   *chunkMinSize,
	}

	// 如果提供了自定义章节，解析它们
	if *chunkCustomSections != "" {
		// 这里可以进一步解析自定义章节，目前简单处理
		fmt.Printf("注：已指定自定义章节，但当前版本尚未实现自定义章节解析\n")
	}

	// 创建分块器
	chunker, err := parser.NewResumeChunker(chunkerConfig)
	if err != nil {
		fmt.Printf("创建简历分块器失败: %v\n", err)
		os.Exit(1)
	}

	// 执行分块
	fmt.Println("2. 开始分块简历文本...")
	startTime = time.Now()

	sections, err := chunker.ChunkResume(ctx, text)
	if err != nil {
		fmt.Printf("分块简历文本失败: %v\n", err)
		os.Exit(1)
	}

	chunkTime := time.Since(startTime)
	fmt.Printf("分块完成! 耗时: %v，识别了 %d 个章节\n", chunkTime, len(sections))

	// 输出结果
	fmt.Println("\n===== 简历分块结果 =====")

	// 根据输出格式进行展示
	if *chunkFormat == "json" {
		// 这里应该输出JSON格式，但当前简单实现
		fmt.Println("注：当前版本尚未实现JSON格式输出")
		formattedText := chunker.FormatSections(sections)
		fmt.Println(formattedText)
	} else {
		// 文本格式输出
		formattedText := chunker.FormatSections(sections)
		fmt.Println(formattedText)
	}

	// 显示统计信息
	fmt.Println("\n===== 处理统计 =====")
	fmt.Printf("PDF大小: %d 字符\n", len(text))
	fmt.Printf("识别章节数: %d\n", len(sections))
	fmt.Printf("PDF提取耗时: %v\n", extractTime)
	fmt.Printf("分块处理耗时: %v\n", chunkTime)
	fmt.Printf("总处理耗时: %v\n", extractTime+chunkTime)

	fmt.Println("\n简历分块完成。")
}
