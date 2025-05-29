package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"ai-agent-go/pkg/agent"
	"ai-agent-go/pkg/parser"

	"github.com/cloudwego/eino/components/model"
)

// 定义LLM分块命令的命令行参数
var (
	llmChunkInputFile = flag.String("llm-chunk-file", "", "要使用LLM分块的PDF文件路径")
	llmOutputFormat   = flag.String("llm-format", "pretty", "输出格式，可选项：pretty, json")
	llmUseEmbedding   = flag.Bool("llm-embed", false, "是否生成向量嵌入（需要配置嵌入模型）")
	llmSaveToFile     = flag.String("llm-save", "", "保存结果到文件路径")
	llmModelName      = flag.String("llm-model", "aliyun-qwen", "使用的LLM模型，当前支持: aliyun-qwen")
)

// 处理LLM分块子命令
func handleLLMChunkCommand() {
	// 默认使用全局的 pdfFilePath
	inputFile := *llmChunkInputFile
	if inputFile == "" {
		inputFile = *pdfFilePath
	}

	if inputFile == "" {
		fmt.Println("错误: 必须提供PDF文件路径。使用 -llm-chunk-file 或 -pdf 参数。")
		flag.Usage()
		os.Exit(1)
	}

	// 创建上下文，添加超时
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second) // LLM调用可能需要更多时间
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

	// 初始化LLM模型
	fmt.Println("2. 初始化LLM模型...")
	startTime = time.Now()

	var llmModel model.ChatModel
	switch *llmModelName {
	case "aliyun-qwen":
		// 从环境变量或配置文件中获取API密钥
		apiKey := os.Getenv("ALIYUN_API_KEY")
		if apiKey == "" {
			fmt.Println("错误: 未找到环境变量ALIYUN_API_KEY，请设置API密钥")
			fmt.Println("可以通过以下方式设置环境变量:")
			fmt.Println("  Windows: set ALIYUN_API_KEY=your_key_here")
			fmt.Println("  Linux/Mac: export ALIYUN_API_KEY=your_key_here")
			os.Exit(1)
		}
		llmModel, err = agent.NewAliyunQwenChatModel(apiKey, "", "") // 使用默认模型和API URL
		if err != nil {
			fmt.Printf("初始化阿里云通义千问模型失败: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("不支持的模型名称: %s\n", *llmModelName)
		os.Exit(1)
	}

	initTime := time.Since(startTime)
	fmt.Printf("初始化LLM模型完成! 耗时: %v\n", initTime)

	// 创建LLM分块器
	chunker := parser.NewLLMResumeChunker(llmModel)

	// 执行LLM分块
	fmt.Println("3. 调用LLM进行简历分块和信息提取...")
	startTime = time.Now()

	sections, basicInfo, err := chunker.ChunkResume(ctx, text)
	if err != nil {
		fmt.Printf("LLM分块失败: %v\n", err)
		os.Exit(1)
	}

	chunkTime := time.Since(startTime)
	fmt.Printf("LLM分块完成! 耗时: %v，识别了 %d 个章节和 %d 个基本信息字段\n",
		chunkTime, len(sections), len(basicInfo))

	// 显示基本信息
	fmt.Println("\n===== 提取的基本信息 =====")
	for k, v := range basicInfo {
		fmt.Printf("%s: %s\n", k, v)
	}

	// 输出分块结果
	fmt.Println("\n===== 简历分块结果 =====")

	// 根据输出格式进行展示
	if *llmOutputFormat == "json" {
		// 创建JSON结构
		result := struct {
			BasicInfo map[string]string       `json:"basic_info"`
			Sections  []*parser.ResumeSection `json:"sections"`
			Metadata  map[string]interface{}  `json:"metadata"`
		}{
			BasicInfo: basicInfo,
			Sections:  sections,
			Metadata:  metadata,
		}

		// 转为JSON
		jsonData, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			fmt.Printf("转换JSON失败: %v\n", err)
			os.Exit(1)
		}

		fmt.Println(string(jsonData))

		// 保存到文件
		if *llmSaveToFile != "" {
			if err := os.WriteFile(*llmSaveToFile, jsonData, 0644); err != nil {
				fmt.Printf("保存到文件失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("结果已保存到文件: %s\n", *llmSaveToFile)
		}
	} else {
		// 结构化文本输出
		fmt.Println("\n----- 基本信息 -----")
		for k, v := range basicInfo {
			fmt.Printf("%s: %s\n", k, v)
		}

		fmt.Println("\n----- 分块章节 -----")
		for i, section := range sections {
			fmt.Printf("\n[%d] %s (%s)\n", i+1, section.Title, section.Type)
			fmt.Printf("内容: %s\n", truncateString(section.Content, 500))
		}

		// 保存到文件
		if *llmSaveToFile != "" {
			var textContent string
			textContent += "====== 基本信息 ======\n"
			for k, v := range basicInfo {
				textContent += fmt.Sprintf("%s: %s\n", k, v)
			}

			textContent += "\n====== 分块章节 ======\n"
			for i, section := range sections {
				textContent += fmt.Sprintf("\n[%d] %s (%s)\n", i+1, section.Title, section.Type)
				textContent += fmt.Sprintf("内容:\n%s\n", section.Content)
			}

			if err := os.WriteFile(*llmSaveToFile, []byte(textContent), 0644); err != nil {
				fmt.Printf("保存到文件失败: %v\n", err)
				os.Exit(1)
			}
			fmt.Printf("结果已保存到文件: %s\n", *llmSaveToFile)
		}
	}

	// 如果需要生成嵌入
	if *llmUseEmbedding {
		fmt.Println("\n暂未实现向量嵌入功能，请等待后续更新")
	}

	// 显示统计信息
	fmt.Println("\n===== 处理统计 =====")
	fmt.Printf("PDF大小: %d 字符\n", len(text))
	fmt.Printf("识别章节数: %d\n", len(sections))
	fmt.Printf("基本信息字段数: %d\n", len(basicInfo))
	fmt.Printf("PDF提取耗时: %v\n", extractTime)
	fmt.Printf("LLM分块耗时: %v\n", chunkTime)
	fmt.Printf("总处理耗时: %v\n", extractTime+initTime+chunkTime)

	fmt.Println("\nLLM简历分块完成。")
}

// 辅助函数：截断字符串
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
