package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"ai-agent-go/pkg/parser"
)

// 定义提取命令的命令行参数
var (
	extractInputFile = flag.String("extract-file", "", "要提取的PDF文件路径")
	extractFormat    = flag.String("extract-format", "text", "输出格式，可选项：text, json")
	extractSaveFile  = flag.String("extract-save", "", "保存提取内容到文件")
)

// 处理提取文本命令
func handleExtractCommand() {
	// 默认使用全局的 pdfFilePath
	inputFile := *extractInputFile
	if inputFile == "" {
		inputFile = *pdfFilePath
	}

	if inputFile == "" {
		fmt.Println("错误: 必须提供PDF文件路径。使用 -extract-file 或 -pdf 参数。")
		flag.Usage()
		os.Exit(1)
	}

	// 获取文件的绝对路径
	absPath, err := filepath.Abs(inputFile)
	if err != nil {
		fmt.Printf("无法获取文件的绝对路径: %v\n", err)
		os.Exit(1)
	}

	// 检查文件是否存在
	_, err = os.Stat(absPath)
	if err != nil {
		fmt.Printf("无法访问文件 %s: %v\n", absPath, err)
		os.Exit(1)
	}

	fmt.Printf("准备处理PDF文件: %s\n", absPath)

	// 创建上下文，添加超时以防止无限等待
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 创建PDF提取器
	extractor, err := parser.NewEinoPDFTextExtractor(ctx)
	if err != nil {
		fmt.Printf("创建PDF提取器失败: %v\n", err)
		os.Exit(1)
	}

	// 提取文本
	fmt.Println("开始从PDF提取文本...")
	startTime := time.Now()

	text, metadata, err := extractor.ExtractFullTextFromPDFFile(ctx, absPath)
	if err != nil {
		fmt.Printf("提取PDF文本失败: %v\n", err)
		os.Exit(1)
	}

	elapsedTime := time.Since(startTime)
	fmt.Printf("提取完成! 耗时: %v\n", elapsedTime)

	// 显示提取结果
	fmt.Printf("\n===== 提取的文本 (总计 %d 字符) =====\n", len(text))

	// 根据maxLen参数决定显示多少文本
	displayText := text
	if *maxLen >= 0 && len(text) > *maxLen {
		displayText = text[:*maxLen] + "...(已截断，使用 -maxlen 参数显示更多)"
	}
	fmt.Println(displayText)

	// 显示部分元数据
	fmt.Println("\n===== 元数据 =====")
	fmt.Printf("文件路径: %s\n", metadata["source_file_path"])

	// 显示其他元数据
	fmt.Println("其他元数据:")
	for k, v := range metadata {
		if k != "source_file_path" {
			fmt.Printf("  %s: %v\n", k, v)
		}
	}

	// 保存到文件
	if *extractSaveFile != "" {
		err = os.WriteFile(*extractSaveFile, []byte(text), 0644)
		if err != nil {
			fmt.Printf("保存到文件失败: %v\n", err)
		} else {
			fmt.Printf("文本已保存到: %s\n", *extractSaveFile)
		}
	}

	fmt.Println("\nPDF处理完成。")
}
