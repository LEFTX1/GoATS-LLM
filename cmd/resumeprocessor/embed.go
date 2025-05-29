package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"ai-agent-go/pkg/agent"
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/parser"
	"ai-agent-go/pkg/storage"
	"ai-agent-go/pkg/types"
)

// 定义嵌入命令的命令行参数
var (
	embedInputFile    string
	embedDimensions   int
	qdrantEndpoint    string
	qdrantCollection  string
	saveVectorsToFile string
	embedUseLLMChunk  bool
)

// init 注册命令行参数
func init() {
	flag.StringVar(&embedInputFile, "embed-file", "", "要进行向量嵌入的PDF文件路径")
	flag.IntVar(&embedDimensions, "embed-dimensions", 1024, "向量维度，可选：1024, 768, 512, 256, 128, 64")
	flag.StringVar(&qdrantEndpoint, "embed-qdrant", "http://localhost:6333", "Qdrant服务器地址")
	flag.StringVar(&qdrantCollection, "embed-collection", "resumes", "Qdrant集合名称")
	flag.StringVar(&saveVectorsToFile, "embed-save-vectors", "", "保存向量到文件路径")
	flag.BoolVar(&embedUseLLMChunk, "embed-use-llm", true, "是否使用LLM进行分块")
}

// 处理嵌入命令
func handleEmbedCommand() {
	// 默认使用全局的 pdfFilePath
	inputFile := embedInputFile
	if inputFile == "" {
		inputFile = *pdfFilePath
	}

	if inputFile == "" {
		fmt.Println("错误: 必须提供PDF文件路径。使用 -embed-file 或 -pdf 参数。")
		flag.Usage()
		os.Exit(1)
	}

	// 创建上下文，添加超时
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	var sections []*parser.ResumeSection
	var basicInfo map[string]string
	var chunkTime time.Duration

	if embedUseLLMChunk {
		// 初始化LLM模型
		fmt.Println("2. 初始化LLM模型...")
		startTime = time.Now()

		// 从环境变量获取API密钥
		apiKey := os.Getenv("ALIYUN_API_KEY")
		if apiKey == "" {
			fmt.Println("错误: 未找到环境变量ALIYUN_API_KEY，请设置API密钥")
			fmt.Println("可以通过以下方式设置环境变量:")
			fmt.Println("  Windows: set ALIYUN_API_KEY=your_key_here")
			fmt.Println("  Linux/Mac: export ALIYUN_API_KEY=your_key_here")
			os.Exit(1)
		}

		llmModel, err := agent.NewAliyunQwenChatModel(apiKey, "", "")
		if err != nil {
			fmt.Printf("初始化阿里云通义千问模型失败: %v\n", err)
			os.Exit(1)
		}

		initTime := time.Since(startTime)
		fmt.Printf("初始化LLM模型完成! 耗时: %v\n", initTime)

		// 创建LLM分块器
		chunker := parser.NewLLMResumeChunker(llmModel)

		// 执行LLM分块
		fmt.Println("3. 调用LLM进行简历分块和信息提取...")
		startTime = time.Now()

		sections, basicInfo, err = chunker.ChunkResume(ctx, text)
		if err != nil {
			fmt.Printf("LLM分块失败: %v\n", err)
			os.Exit(1)
		}

		chunkTime = time.Since(startTime)
		fmt.Printf("LLM分块完成! 耗时: %v，识别了 %d 个章节和 %d 个基本信息字段\n",
			chunkTime, len(sections), len(basicInfo))

		fmt.Println("\n===== 提取的基本信息 =====")
		for k, v := range basicInfo {
			fmt.Printf("%s: %s\n", k, v)
		}

		fmt.Println("\n===== 简历分块结果 =====")
		for i, section := range sections {
			fmt.Printf("[%d] %s (%s)\n", i+1, section.Title, section.Type)
			fmt.Printf("内容: %s\n\n", truncateString(section.Content, 200))
		}
	} else {
		// 使用基本分块器
		fmt.Println("2. 使用简单分块简历...")
		startTime = time.Now()

		basicInfo = make(map[string]string)
		// 简单将整个文本作为一个章节
		sections = []*parser.ResumeSection{
			{
				Type:    parser.SectionFullText,
				Title:   "完整简历",
				Content: text,
			},
		}

		chunkTime = time.Since(startTime)
		fmt.Printf("分块完成! 耗时: %v，使用单一章节\n", chunkTime)

		fmt.Println("\n===== 简历分块结果 =====")
		fmt.Println("[1] 完整简历 (FULL_TEXT)")
		fmt.Printf("内容: %s\n\n", truncateString(text, 200))
	}

	// 创建阿里云Embedding服务
	fmt.Println("4. 初始化向量嵌入服务...")
	startTime = time.Now()

	apiKey := os.Getenv("ALIYUN_API_KEY")
	if apiKey == "" {
		fmt.Println("错误: 未找到环境变量ALIYUN_API_KEY，请设置API密钥")
		os.Exit(1)
	}

	// 创建阿里云Embedder
	embedder, err := parser.NewAliyunEmbedder(apiKey,
		parser.WithAliyunDimensions(embedDimensions),
		parser.WithAliyunModel("text-embedding-v3"))
	if err != nil {
		fmt.Printf("创建Embedder失败: %v\n", err)
		os.Exit(1)
	}

	// 创建简历嵌入器
	resumeEmbedder := parser.NewStandardResumeEmbedder(embedder,
		parser.WithIncludeBasicInfo(true),
		parser.WithIncludeContent(false),
		parser.WithIncludeChunkType(true))

	initTime := time.Since(startTime)
	fmt.Printf("初始化向量嵌入服务完成! 耗时: %v\n", initTime)

	// 嵌入向量
	fmt.Println("5. 开始向量嵌入...")
	startTime = time.Now()

	vectors, err := resumeEmbedder.EmbedResumeChunks(ctx, sections, basicInfo)
	if err != nil {
		fmt.Printf("向量嵌入失败: %v\n", err)
		os.Exit(1)
	}

	embedTime := time.Since(startTime)
	fmt.Printf("向量嵌入完成! 耗时: %v，生成了 %d 个向量\n", embedTime, len(vectors))

	// 保存向量到文件
	if saveVectorsToFile != "" {
		fmt.Printf("保存向量到文件 %s...\n", saveVectorsToFile)
		vectorData, err := json.MarshalIndent(vectors, "", "  ")
		if err != nil {
			fmt.Printf("序列化向量失败: %v\n", err)
		} else {
			if err := os.WriteFile(saveVectorsToFile, vectorData, 0644); err != nil {
				fmt.Printf("保存向量到文件失败: %v\n", err)
			} else {
				fmt.Println("向量成功保存到文件!")
			}
		}
	}

	// 存储到Qdrant
	fmt.Println("6. 存储向量到Qdrant...")
	startTime = time.Now()

	// 创建临时配置
	cfg := &config.Config{}
	cfg.Qdrant.Endpoint = qdrantEndpoint
	cfg.Qdrant.Collection = qdrantCollection
	cfg.Qdrant.Dimension = embedDimensions

	vectorStore, err := storage.NewResumeVectorStore(cfg)
	if err != nil {
		fmt.Printf("连接Qdrant失败: %v\n", err)
		os.Exit(1)
	}

	// 提取resumeID
	resumeID := "resume_" + time.Now().Format("20060102150405")
	if len(basicInfo) > 0 {
		if name, ok := basicInfo["name"]; ok {
			resumeID = "resume_" + name
		}
	}

	// 转换为ResumeChunk
	chunks := make([]types.ResumeChunk, len(vectors))
	embeddings := make([][]float32, len(vectors))

	for i, vector := range vectors {
		// 转换vector到float32
		float32Vector := make([]float32, len(vector.Vector))
		for j, val := range vector.Vector {
			float32Vector[j] = float32(val)
		}
		embeddings[i] = float32Vector

		// 创建chunk
		chunkType := "UNKNOWN"
		content := ""
		if vector.Section != nil {
			chunkType = string(vector.Section.Type)
			content = vector.Section.Content
		}

		chunks[i] = types.ResumeChunk{
			ChunkID:         i,
			Content:         content,
			ChunkType:       chunkType,
			ImportanceScore: 1,
			Metadata:        types.ChunkMetadata{},
		}

		// 转换metadata
		if skills, ok := vector.Metadata["skills"].([]string); ok {
			chunks[i].Metadata.Skills = skills
		}
		if exp, ok := vector.Metadata["experience_years"].(float64); ok {
			chunks[i].Metadata.ExperienceYears = int(exp)
		}
		if edu, ok := vector.Metadata["education_level"].(string); ok {
			chunks[i].Metadata.EducationLevel = edu
		}
	}

	// 存储向量
	_, err = vectorStore.StoreResumeVectors(ctx, resumeID, chunks, embeddings)
	if err != nil {
		fmt.Printf("存储向量失败: %v\n", err)
		os.Exit(1)
	}

	storeTime := time.Since(startTime)
	fmt.Printf("存储向量完成! 耗时: %v\n", storeTime)

	// 输出统计信息
	fmt.Println("\n===== 处理统计 =====")
	fmt.Printf("PDF大小: %d 字符\n", len(text))
	fmt.Printf("章节数: %d\n", len(sections))
	fmt.Printf("向量数: %d\n", len(vectors))
	fmt.Printf("PDF提取耗时: %v\n", extractTime)
	fmt.Printf("分块处理耗时: %v\n", chunkTime)
	fmt.Printf("向量嵌入耗时: %v\n", embedTime)
	fmt.Printf("向量存储耗时: %v\n", storeTime)
	fmt.Printf("总处理耗时: %v\n", extractTime+chunkTime+embedTime+storeTime)

	fmt.Println("\n向量嵌入和存储完成。已存入Qdrant集合：" + qdrantCollection)
}
