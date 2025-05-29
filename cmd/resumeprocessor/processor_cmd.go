package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"ai-agent-go/pkg/processor"
)

// 定义处理命令的命令行参数
var (
	processorInputFile  = flag.String("process-file", "", "要处理的PDF文件路径")
	processorUseLLM     = flag.Bool("process-llm", false, "是否使用LLM进行分块")
	processorEmbed      = flag.Bool("process-embed", false, "是否生成向量嵌入")
	processorStore      = flag.Bool("process-store", true, "是否存储向量到Qdrant")
	processorDimension  = flag.Int("process-dimension", 1024, "向量维度")
	processorQdrant     = flag.String("process-qdrant", "http://localhost:6333", "Qdrant服务器地址")
	processorCollection = flag.String("process-collection", "resumes", "Qdrant集合名称")
	processorOutputJSON = flag.String("process-output", "", "输出结果到JSON文件")
)

// 处理命令
func handleProcessCommand() {
	// 默认使用全局的 pdfFilePath
	inputFile := *processorInputFile
	if inputFile == "" {
		inputFile = *pdfFilePath
	}

	if inputFile == "" {
		fmt.Println("错误: 必须提供PDF文件路径。使用 -process-file 或 -pdf 参数。")
		flag.Usage()
		os.Exit(1)
	}

	// 创建上下文，添加超时
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// 获取API密钥
	apiKey := os.Getenv("ALIYUN_API_KEY")
	if (*processorUseLLM || *processorEmbed) && apiKey == "" {
		fmt.Println("错误: 未找到环境变量ALIYUN_API_KEY，请设置API密钥")
		fmt.Println("可以通过以下方式设置环境变量:")
		fmt.Println("  Windows: set ALIYUN_API_KEY=your_key_here")
		fmt.Println("  Linux/Mac: export ALIYUN_API_KEY=your_key_here")
		os.Exit(1)
	}

	// 创建处理器
	var proc processor.Processor
	var err error

	if *processorEmbed && *processorStore {
		// 创建完整处理器（包含嵌入和存储）
		fmt.Println("1. 初始化完整处理器...")
		proc, err = processor.CreateFullProcessor(
			ctx,
			apiKey,
			*processorQdrant,
			*processorCollection,
			*processorDimension,
		)
	} else {
		// 创建基本处理器
		fmt.Println("1. 初始化基本处理器...")
		proc, err = processor.CreateDefaultProcessor(ctx)
	}

	if err != nil {
		fmt.Printf("创建处理器失败: %v\n", err)
		os.Exit(1)
	}

	// 配置处理选项
	options := processor.ProcessOptions{
		InputFile:      inputFile,
		UseLLM:         *processorUseLLM,
		PreserveFormat: false,
		Dimensions:     *processorDimension,
	}

	// 执行处理
	fmt.Println("2. 开始处理简历...")
	startTime := time.Now()

	result, err := proc.Process(ctx, options)
	if err != nil {
		fmt.Printf("处理简历失败: %v\n", err)
		os.Exit(1)
	}

	totalTime := time.Since(startTime)
	fmt.Printf("处理完成! 总耗时: %v\n", totalTime)

	// 显示处理结果
	fmt.Printf("\n===== 处理结果 =====\n")
	fmt.Printf("提取文本长度: %d 字符\n", len(result.Text))
	fmt.Printf("识别章节数: %d\n", len(result.Sections))
	fmt.Printf("基本信息字段数: %d\n", len(result.BasicInfo))
	fmt.Printf("生成向量数: %d\n", len(result.Vectors))

	// 显示基本信息
	if len(result.BasicInfo) > 0 {
		fmt.Println("\n----- 基本信息 -----")
		for k, v := range result.BasicInfo {
			fmt.Printf("%s: %s\n", k, v)
		}
	}

	// 显示章节信息
	if len(result.Sections) > 0 {
		fmt.Println("\n----- 章节信息 -----")
		for i, section := range result.Sections {
			fmt.Printf("[%d] %s (%s)\n", i+1, section.Title, section.Type)
			if len(section.Content) > 100 {
				fmt.Printf("内容: %s...\n\n", section.Content[:100])
			} else {
				fmt.Printf("内容: %s\n\n", section.Content)
			}
		}
	}

	// 显示处理统计
	fmt.Println("\n----- 处理统计 -----")
	for k, v := range result.Stats {
		fmt.Printf("%s: %v\n", k, v)
	}

	// 输出JSON结果
	if *processorOutputJSON != "" {
		fmt.Printf("\n保存结果到JSON文件: %s\n", *processorOutputJSON)

		// 创建可序列化的结构
		outputData := struct {
			BasicInfo map[string]string `json:"basic_info"`
			Sections  []struct {
				Type    string `json:"type"`
				Title   string `json:"title"`
				Content string `json:"content"`
			} `json:"sections"`
			Stats map[string]interface{} `json:"stats"`
		}{
			BasicInfo: result.BasicInfo,
			Stats:     result.Stats,
		}

		// 转换章节
		outputData.Sections = make([]struct {
			Type    string `json:"type"`
			Title   string `json:"title"`
			Content string `json:"content"`
		}, len(result.Sections))

		for i, section := range result.Sections {
			outputData.Sections[i].Type = string(section.Type)
			outputData.Sections[i].Title = section.Title
			outputData.Sections[i].Content = section.Content
		}

		// 序列化为JSON
		jsonData, err := json.MarshalIndent(outputData, "", "  ")
		if err != nil {
			fmt.Printf("序列化JSON失败: %v\n", err)
		} else {
			err = os.WriteFile(*processorOutputJSON, jsonData, 0644)
			if err != nil {
				fmt.Printf("保存JSON文件失败: %v\n", err)
			} else {
				fmt.Println("成功保存JSON结果!")
			}
		}
	}

	fmt.Println("\n简历处理完成。")
}
