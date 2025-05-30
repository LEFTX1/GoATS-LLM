package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/parser"
)

// handleEmbedCommand 处理简历向量嵌入命令
func handleEmbedCommand() {
	if *pdfFilePath == "" {
		fmt.Println("错误: 必须指定PDF文件路径")
		flag.Usage()
		os.Exit(1)
	}

	// 检查文件是否存在
	if _, err := os.Stat(*pdfFilePath); os.IsNotExist(err) {
		fmt.Printf("错误: 文件不存在: %s\n", *pdfFilePath)
		os.Exit(1)
	}

	fmt.Printf("处理PDF文件: %s\n", *pdfFilePath)

	// 1. 加载配置
	cfg, err := config.LoadConfig("")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 2. 提取PDF文本
	ctx := context.Background()
	pdfExtractor, err := parser.NewEinoPDFTextExtractor(ctx)
	if err != nil {
		log.Fatalf("创建PDF提取器失败: %v", err)
	}

	text, metadata, err := pdfExtractor.ExtractFullTextFromPDFFile(ctx, *pdfFilePath)
	if err != nil {
		log.Fatalf("提取PDF文本失败: %v", err)
	}

	// 记录PDF元数据
	fmt.Println("PDF元数据:")
	for key, value := range metadata {
		fmt.Printf("  %s: %v\n", key, value)
	}

	// 如果有maxLen限制，截取文本
	displayText := text
	if *maxLen > 0 && len(text) > *maxLen {
		displayText = text[:*maxLen] + "..."
	}
	fmt.Printf("提取的文本(%d字符):\n%s\n", len(text), displayText)

	// 3. 使用规则分块
	chunkerConfig := parser.ChunkerConfig{
		PreserveFormat: false,
		MinChunkSize:   0,
	}
	chunker, err := parser.NewResumeChunker(chunkerConfig)
	if err != nil {
		log.Fatalf("创建分块器失败: %v", err)
	}

	sections, err := chunker.ChunkResume(ctx, text)
	if err != nil {
		log.Fatalf("分块失败: %v", err)
	}

	fmt.Printf("成功分块: %d个分块\n", len(sections))

	// 4. 创建嵌入器
	embeddingCfg := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, embeddingCfg)
	if err != nil {
		log.Fatalf("创建嵌入器失败: %v", err)
	}

	// 5. 创建简历嵌入器
	resumeEmbedder := parser.NewStandardResumeEmbedder(embedder,
		parser.WithIncludeContent(true),
		parser.WithIncludeChunkType(true))

	// 6. 生成向量嵌入
	basicInfo := map[string]string{
		"filename": filepath.Base(*pdfFilePath),
	}

	vectors, err := resumeEmbedder.EmbedResumeChunks(ctx, sections, basicInfo)
	if err != nil {
		log.Fatalf("生成向量嵌入失败: %v", err)
	}

	fmt.Printf("成功生成%d个向量嵌入\n", len(vectors))

	// 7. 显示向量信息
	for i, v := range vectors {
		// 限制只显示前几个
		if i >= 3 {
			fmt.Printf("(更多向量省略...)\n")
			break
		}

		fmt.Printf("向量 #%d:\n", i+1)
		fmt.Printf("  分块类型: %s\n", v.Section.Type)
		fmt.Printf("  分块标题: %s\n", v.Section.Title)

		// 显示前几个元数据字段
		fmt.Printf("  元数据:\n")
		count := 0
		for k, val := range v.Metadata {
			fmt.Printf("    %s: %v\n", k, val)
			count++
			if count >= 5 {
				fmt.Printf("    (更多元数据省略...)\n")
				break
			}
		}

		// 显示向量维度和部分值
		fmt.Printf("  向量维度: %d\n", len(v.Vector))
		if len(v.Vector) > 0 {
			preview := ""
			for j := 0; j < 5 && j < len(v.Vector); j++ {
				preview += fmt.Sprintf("%.4f ", v.Vector[j])
			}
			fmt.Printf("  向量值(前5): %s...\n", preview)
		}
		fmt.Println()
	}

	fmt.Println("向量嵌入处理完成")
}
