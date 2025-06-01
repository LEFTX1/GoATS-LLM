package main

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/handler"
	"ai-agent-go/internal/logger"
	parser2 "ai-agent-go/internal/parser"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// @title           AI Agent API
// @version         1.0
// @description     AI Agent API for resume processing
// @BasePath  /api/v1
func main() {
	// 初始化日志系统
	initLogger()

	// 1. 加载配置文件
	cfg, err := config.LoadConfig("")
	if err != nil {
		logger.Fatal().Err(err).Msg("加载配置文件失败")
	}

	// 2. 初始化存储管理器
	ctx := context.Background()
	storageManager, err := storage.NewStorage(ctx, cfg)
	if err != nil {
		logger.Fatal().Err(err).Msg("初始化存储管理器失败")
	}
	defer storageManager.Close()

	// 3. 初始化业务处理器
	resumeHandler, err := initializeHandler(cfg, storageManager)
	if err != nil {
		logger.Fatal().Err(err).Msg("初始化简历处理器失败")
	}
	logger.Info().Msg("简历处理器初始化成功")

	// 4. 启动简历处理消费者
	// 4.1 上传消费者 (批量)
	go func() {
		err := resumeHandler.StartResumeUploadConsumer(context.Background(), 10, 5*time.Second)
		if err != nil {
			logger.Fatal().Err(err).Msg("启动简历上传消费者失败")
		}
	}()

	// 4.2 LLM解析消费者
	go func() {
		err := resumeHandler.StartLLMParsingConsumer(context.Background(), 5)
		if err != nil {
			logger.Fatal().Err(err).Msg("启动LLM解析消费者失败")
		}
	}()

	// 4.3 启动MD5记录清理任务
	go func() {
		resumeHandler.StartMD5CleanupTask(context.Background())
	}()

	// 5. 创建HTTP服务器
	h := server.Default(
		server.WithHostPorts(cfg.Server.Address),
	)

	// 6. 添加路由
	api := h.Group("/api/v1")
	api.POST("/resume/upload", func(c context.Context, ctx *app.RequestContext) {
		// 获取上传的文件
		fileHeader, err := ctx.FormFile("file")
		if err != nil {
			ctx.JSON(consts.StatusBadRequest, utils.H{"error": "文件未找到"})
			return
		}

		// 获取目标岗位ID
		targetJobID := ctx.PostForm("target_job_id")
		// 获取来源渠道
		sourceChannel := ctx.PostForm("source_channel")
		if sourceChannel == "" {
			sourceChannel = "web_upload"
		}

		// 打开文件
		file, err := fileHeader.Open()
		if err != nil {
			ctx.JSON(consts.StatusInternalServerError, utils.H{"error": "打开文件失败"})
			return
		}
		defer file.Close()

		// 处理上传
		resp, err := resumeHandler.HandleResumeUpload(
			c,
			file,
			fileHeader.Size,
			fileHeader.Filename,
			targetJobID,
			sourceChannel,
		)
		if err != nil {
			ctx.JSON(consts.StatusInternalServerError, utils.H{"error": err.Error()})
			return
		}

		ctx.JSON(consts.StatusOK, resp)
	})

	// 添加健康检查
	api.GET("/health", func(c context.Context, ctx *app.RequestContext) {
		ctx.JSON(consts.StatusOK, utils.H{"status": "ok"})
	})

	// 7. 启动HTTP服务器
	go func() {
		if err := h.Run(); err != nil {
			logger.Fatal().Err(err).Msg("启动HTTP服务器失败")
		}
	}()

	// 8. 等待终止信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	logger.Info().Msg("接收到终止信号，正在优雅退出...")

	// 9. 优雅关闭HTTP服务器
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.Shutdown(shutdownCtx); err != nil {
		logger.Fatal().Err(err).Msg("服务器关闭失败")
	}

	logger.Info().Msg("优雅退出完成")
}

// 初始化日志系统
func initLogger() {
	// 默认开发环境使用美化输出，生产环境使用JSON格式
	isProduction := os.Getenv("ENV") == "production"

	// 尝试加载配置文件
	cfg, err := config.LoadConfig("")

	logConfig := logger.Config{
		Level:        "debug",
		Format:       "pretty",
		TimeFormat:   time.RFC3339,
		ReportCaller: true,
	}

	// 如果配置文件成功加载，使用配置文件中的日志设置
	if err == nil && cfg != nil {
		logConfig.Level = cfg.Logger.Level
		logConfig.Format = cfg.Logger.Format
		logConfig.TimeFormat = cfg.Logger.TimeFormat
		logConfig.ReportCaller = cfg.Logger.ReportCaller
	} else if isProduction {
		// 如果配置文件加载失败但是是生产环境，使用生产环境的默认设置
		logConfig.Level = "info"
		logConfig.Format = "json"
		logConfig.ReportCaller = false
	}

	logger.Init(logConfig)

	// 设置一些全局的字段
	logger.Logger = logger.Logger.With().
		Str("app", "ai-agent-go").
		Str("version", "1.0.0").
		Logger()
}

func initializeHandler(cfg *config.Config, storageManager *storage.Storage) (*handler.ResumeHandler, error) {
	// 检查存储管理器
	if storageManager == nil {
		return nil, fmt.Errorf("存储管理器未初始化")
	}
	if storageManager.MinIO == nil {
		return nil, fmt.Errorf("MinIO实例未初始化")
	}
	if storageManager.RabbitMQ == nil {
		return nil, fmt.Errorf("RabbitMQ实例未初始化")
	}
	if storageManager.MySQL == nil {
		return nil, fmt.Errorf("MySQL实例未初始化")
	}

	// 创建处理器组件集合
	processorComponents, err := processor.CreateProcessorFromConfig(context.Background(), cfg)
	if err != nil {
		logger.Warn().Err(err).Msg("从配置创建处理器组件集合失败，将使用默认处理器")
		// 使用默认处理器
		processorComponents, _ = processor.CreateDefaultProcessor(context.Background(), cfg)
	}

	// 如果配置了Aliyun API密钥，设置LLM相关组件
	if cfg.Aliyun.APIKey != "" {
		// 创建LLM模型
		aliyunChatModel, err := agent.NewAliyunQwenChatModel(
			cfg.Aliyun.APIKey,
			cfg.Aliyun.Model,
			cfg.Aliyun.APIURL,
		)
		if err != nil {
			logger.Warn().Err(err).Msg("初始化LLM模型失败")
		} else {
			// 创建LLM分块器并设置到处理器中
			llmChunker := parser2.NewLLMResumeChunker(aliyunChatModel)
			// 创建LLM评估器并设置到处理器中
			llmEvaluator := parser2.NewLLMJobEvaluator(aliyunChatModel)

			// 应用到组件集合
			processor.WithResumeChunker(llmChunker)(processorComponents)
			processor.WithJobMatchEvaluator(llmEvaluator)(processorComponents)
		}
	}

	// 创建处理器，直接传入存储管理器
	resumeHandler := handler.NewResumeHandler(
		cfg,
		storageManager,
		processorComponents,
	)

	return resumeHandler, nil
}
