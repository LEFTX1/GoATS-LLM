package main

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/api/handler"
	"ai-agent-go/internal/api/router"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/logger"
	parser2 "ai-agent-go/internal/parser"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/pkg/ratelimit"
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/hertz/pkg/app/server"
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
	// 4.1 上传消费者
	go func() {
		// 从配置中读取上传消费者的工作线程数，默认为10
		uploadConsumerWorkers := 10
		if workers, ok := cfg.RabbitMQ.ConsumerWorkers["upload_consumer_workers"]; ok {
			uploadConsumerWorkers = workers
		}
		// 从配置中读取上传消费者的批量处理超时时间，默认为5秒
		batchTimeout := 5 * time.Second
		if timeoutStr, ok := cfg.RabbitMQ.BatchTimeouts["upload_batch_timeout"]; ok {
			if parsedTimeout, parseErr := time.ParseDuration(timeoutStr); parseErr == nil {
				batchTimeout = parsedTimeout
			}
		}
		err := resumeHandler.StartResumeUploadConsumer(context.Background(), uploadConsumerWorkers, batchTimeout)
		if err != nil {
			logger.Fatal().Err(err).Msg("启动简历上传消费者失败")
		}
	}()

	// 4.2 LLM解析消费者
	go func() {
		// 从配置中读取LLM解析消费者的工作线程数，默认为5
		llmConsumerWorkers := 5
		if workers, ok := cfg.RabbitMQ.ConsumerWorkers["llm_consumer_workers"]; ok {
			llmConsumerWorkers = workers
		}
		err := resumeHandler.StartLLMParsingConsumer(context.Background(), llmConsumerWorkers)
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

	// 6. 使用新的router包注册路由
	router.RegisterRoutes(h, resumeHandler)

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
	if err == nil && cfg != nil && cfg.Logger.Level != "" { // 检查cfg.Logger.Level是否为空
		logConfig.Level = cfg.Logger.Level
		logConfig.Format = cfg.Logger.Format
		logConfig.TimeFormat = cfg.Logger.TimeFormat
		logConfig.ReportCaller = cfg.Logger.ReportCaller
	} else if isProduction {
		// 如果配置文件加载失败但是是生产环境，使用生产环境的默认设置
		logConfig.Level = "info"
		logConfig.Format = "json"
		logConfig.ReportCaller = false
	} else if err != nil {
		logger.Warn().Err(err).Msg("加载配置文件失败，将使用默认日志配置")
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
		// 为分块器创建原始LLM模型
		chunkerModelName := cfg.LLMParser.ModelName
		if chunkerModelName == "" {
			chunkerModelName = cfg.Aliyun.Model
		}

		// 处理分块器专用参数
		chunkerQPM := cfg.LLMParser.QPM
		chunkerMaxRetries := cfg.LLMParser.MaxRetries
		chunkerRetryWait := time.Duration(cfg.LLMParser.RetryWaitSeconds) * time.Second

		// 创建原始LLM模型（分块器）
		baseChunkerModel, err := agent.NewAliyunQwenChatModel(
			cfg.Aliyun.APIKey,
			chunkerModelName,
			cfg.Aliyun.APIURL,
		)
		if err != nil {
			logger.Warn().Err(err).Msg("创建分块器LLM模型失败")
		} else {
			// 直接在源文件中使用构造函数方式创建带限流的LLM模型
			limitedChunkerModel := ratelimit.NewLLMWithRateLimit(
				baseChunkerModel,
				chunkerModelName,
				cfg.ModelQPMLimits,
				chunkerQPM,
				chunkerMaxRetries,
				chunkerRetryWait,
			)

			// 记录日志
			logger.Info().
				Str("model", chunkerModelName).
				Int("qpm", chunkerQPM).
				Int("maxRetries", chunkerMaxRetries).
				Dur("retryWait", chunkerRetryWait).
				Msg("创建带限流的LLM分块器模型")

			// 创建LLM分块器并设置到处理器中
			llmChunker := parser2.NewLLMResumeChunker(limitedChunkerModel)
			processor.WithResumeChunker(llmChunker)(processorComponents)
		}

		// 为评估器创建原始LLM模型
		evaluatorModelName := cfg.JobEvaluator.ModelName
		if evaluatorModelName == "" {
			evaluatorModelName = cfg.Aliyun.Model
		}

		// 检查任务专用模型配置
		if cfg.Aliyun.TaskModels != nil {
			if jobEvalModel, ok := cfg.Aliyun.TaskModels["job_evaluate"]; ok && jobEvalModel != "" {
				evaluatorModelName = jobEvalModel
			}
		}

		// 处理评估器专用参数
		evaluatorQPM := cfg.JobEvaluator.QPM
		evaluatorMaxRetries := cfg.JobEvaluator.MaxRetries
		evaluatorRetryWait := time.Duration(cfg.JobEvaluator.RetryWaitSeconds) * time.Second

		// 创建原始LLM模型（评估器）
		baseEvaluatorModel, err := agent.NewAliyunQwenChatModel(
			cfg.Aliyun.APIKey,
			evaluatorModelName,
			cfg.Aliyun.APIURL,
		)
		if err != nil {
			logger.Warn().Err(err).Msg("创建评估器LLM模型失败")
		} else {
			// 直接在源文件中使用构造函数方式创建带限流的LLM模型
			limitedEvaluatorModel := ratelimit.NewLLMWithRateLimit(
				baseEvaluatorModel,
				evaluatorModelName,
				cfg.ModelQPMLimits,
				evaluatorQPM,
				evaluatorMaxRetries,
				evaluatorRetryWait,
			)

			// 记录日志
			logger.Info().
				Str("model", evaluatorModelName).
				Int("qpm", evaluatorQPM).
				Int("maxRetries", evaluatorMaxRetries).
				Dur("retryWait", evaluatorRetryWait).
				Msg("创建带限流的LLM评估器模型")

			// 创建LLM评估器并设置到处理器中
			llmEvaluator := parser2.NewLLMJobEvaluator(limitedEvaluatorModel)
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
