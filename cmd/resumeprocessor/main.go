package main

import (
	"flag"
	"fmt"
	"os"
)

// 命令行参数定义
var (
	pdfFilePath = flag.String("pdf", "", "PDF简历文件路径 (必填)")
	maxLen      = flag.Int("maxlen", 1000, "显示的文本最大长度，设为-1显示全部")
	command     = flag.String("cmd", "extract", "执行的命令: extract=仅提取文本, chunk=分块文本, llm-chunk=LLM分块文本, embed=向量嵌入, process=使用新架构处理")
)

func main() {
	// 解析命令行参数
	flag.Parse()

	// 根据命令执行不同的功能
	switch *command {
	case "extract":
		handleExtractCommand()
	case "chunk":
		handleChunkCommand()
	case "llm-chunk":
		handleLLMChunkCommand()
	case "embed":
		handleEmbedCommand()
	case "process":
		handleProcessCommand()
	default:
		fmt.Printf("错误: 未知命令 '%s'。支持的命令: extract, chunk, llm-chunk, embed, process\n", *command)
		flag.Usage()
		os.Exit(1)
	}
}
