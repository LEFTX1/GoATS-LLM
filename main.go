package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ai-agent-go/pkg/agent"
	"ai-agent-go/pkg/api"
	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/handler"
	"ai-agent-go/pkg/parser"
	"ai-agent-go/pkg/storage/singleton"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func main() {
	ctx := context.Background()

	// 1. 加载配置
	cfg, err := config.LoadConfig("config.yaml")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	log.Printf("配置加载成功")

	// 2. 初始化各种适配器 - 使用单例模式
	// 2.1 MinIO
	minioAdapter, err := singleton.GetMinIOAdapter(&cfg.MinIO)
	if err != nil {
		log.Fatalf("初始化MinIO适配器失败: %v", err)
	}
	log.Printf("MinIO适配器初始化成功")

	// 2.2 RabbitMQ
	mqAdapter, err := singleton.GetRabbitMQAdapter(&cfg.RabbitMQ)
	if err != nil {
		log.Fatalf("初始化RabbitMQ适配器失败: %v", err)
	}
	log.Printf("RabbitMQ适配器初始化成功")

	// 2.3 MySQL
	mysqlAdapter, err := singleton.GetMySQLAdapter(&cfg.MySQL)
	if err != nil {
		log.Fatalf("初始化MySQL适配器失败: %v", err)
	}
	log.Printf("MySQL适配器初始化成功")

	// 2.4 Qdrant (如果需要)
	// 初始化Qdrant客户端，但不使用返回的实例
	// 这样可以确保单例已被创建，方便其他组件使用
	_, err = singleton.GetQdrantClient(cfg)
	if err != nil {
		log.Printf("初始化Qdrant客户端失败: %v", err)
		log.Printf("Qdrant不是必需的，将继续运行其他组件")
	} else {
		log.Printf("Qdrant客户端初始化成功")
	}

	// 3. 初始化模型客户端
	// 3.1 阿里云通义千问LLM客户端
	llm, err := agent.NewAliyunQwenChatModel(cfg.Aliyun.APIKey, cfg.Aliyun.Model, cfg.Aliyun.APIURL)
	if err != nil {
		log.Fatalf("初始化LLM客户端失败: %v", err)
	}
	log.Printf("LLM客户端初始化成功")

	// 3.2 PDF解析器
	pdfExtractor, err := parser.NewEinoPDFTextExtractor(ctx)
	if err != nil {
		log.Fatalf("初始化PDF解析器失败: %v", err)
	}
	log.Printf("PDF解析器初始化成功")

	// 3.3 LLM简历分块器
	llmChunker := parser.NewLLMResumeChunker(llm)
	log.Printf("LLM简历分块器初始化成功")

	// 4. 初始化业务处理器
	resumeHandler := handler.NewResumeHandler(
		cfg,
		minioAdapter,
		mqAdapter,
		mysqlAdapter,
		pdfExtractor,
		llmChunker,
	)
	log.Printf("简历处理器初始化成功")

	// 5. 初始化API处理器
	resumeAPIHandler := api.NewResumeAPIHandler(resumeHandler)
	log.Printf("API处理器初始化成功")

	// 6. 启动消费者
	// 6.1 简历上传消费者
	if err := resumeHandler.StartResumeUploadConsumer(ctx, 10, 5*time.Second); err != nil {
		log.Fatalf("启动简历上传消费者失败: %v", err)
	}
	log.Printf("简历上传消费者启动成功")

	// 6.2 LLM解析消费者
	if err := resumeHandler.StartLLMParsingConsumer(ctx, 2); err != nil {
		log.Fatalf("启动LLM解析消费者失败: %v", err)
	}
	log.Printf("LLM解析消费者启动成功")

	// 7. 启动Hertz HTTP服务器
	h := server.Default(server.WithHostPorts(cfg.Server.Address))

	// 定义API路由组
	apiGroup := h.Group("/api/v1")
	{
		// 简历上传接口
		apiGroup.POST("/resume/upload", resumeAPIHandler.UploadResume)
		// 可以添加健康检查等其他接口
		apiGroup.GET("/health", func(c context.Context, ctx *app.RequestContext) {
			ctx.JSON(consts.StatusOK, utils.H{"status": "OK"})
		})
	}

	log.Printf("HTTP服务器正在启动，监听地址: %s", cfg.Server.Address)
	go func() {
		h.Spin()
	}()

	// 8. 等待信号退出
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("收到退出信号，正在关闭资源...")

	// 9. 清理资源
	// 优雅关闭Hertz服务器
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Shutdown(shutdownCtx); err != nil {
		log.Printf("Hertz服务器关闭失败: %v", err)
	}
	log.Printf("Hertz服务器已关闭")

	// 所有资源都由单例模式管理，只需要重置单例即可释放资源
	// 这里显式关闭RabbitMQ和MySQL连接以确保正确释放
	if err := mqAdapter.Close(); err != nil {
		log.Printf("关闭RabbitMQ适配器失败: %v", err)
	}

	if err := mysqlAdapter.Close(); err != nil {
		log.Printf("关闭MySQL适配器失败: %v", err)
	}

	// 重置所有单例
	singleton.ResetMinIOAdapter()
	singleton.ResetMySQLAdapter()
	singleton.ResetRabbitMQAdapter()
	singleton.ResetQdrantClient()

	log.Printf("优雅退出完成")
}
