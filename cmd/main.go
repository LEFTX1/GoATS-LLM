package main // 声明可执行程序的主包

import ( // 导入所需的外部和内部包
	"ai-agent-go/internal/api/handler" // 引入 API 处理器包
	"ai-agent-go/internal/api/router"  // 引入路由注册包
	"ai-agent-go/internal/config"      // 引入应用配置包
	"ai-agent-go/internal/outbox"      // 引入消息中继包
	"ai-agent-go/internal/parser"      // 引入解析器包，用于文档解析
	"ai-agent-go/internal/processor"   // 引入处理器包，包含业务逻辑实现
	"ai-agent-go/internal/storage"     // 引入存储管理包，负责数据库和消息队列
	"context"                          // 引入上下文包，用于传递取消信号和超时信息
	"fmt"                              // 引入 fmt 包，用于格式化字符串
	"io"                               // 引入 IO 包，用于 I/O 操作
	"log"                              // 引入标准日志包
	"os"                               // 引入操作系统包，用于文件和环境变量操作
	"os/signal"                        // 引入信号处理包，用于捕获系统信号
	"path/filepath"                    // 引入文件路径处理包
	"syscall"                          // 引入系统调用包，用于处理低级系统信号
	"time"                             // 引入时间包

	"github.com/rs/zerolog"          // 引入 Zerolog 日志库核心包
	zlog "github.com/rs/zerolog/log" // 引入 Zerolog 全局日志实例

	// Eino imports - 保留必要部分
	"github.com/cloudwego/eino/components/model" // 引入 Eino 模型接口定义

	// Hertz 及相关导入
	"github.com/cloudwego/hertz/pkg/app"        // 引入 Hertz 应用上下文
	"github.com/cloudwego/hertz/pkg/app/server" // 引入 Hertz 服务器构建器
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/spf13/pflag" // 引入 pflag，用于命令行参数解析

	appCoreLogger "ai-agent-go/internal/logger" // 引入应用核心日志器别名

	"ai-agent-go/internal/agent" // 引入 agent 包，用于处理阿里云千问模型

	"github.com/cloudwego/hertz/pkg/app/middlewares/server/recovery"
	glog "github.com/cloudwego/hertz/pkg/common/hlog"           // 引入 Hertz glog 日志适配
	hertzadapter "github.com/hertz-contrib/logger/zerolog"      // 引入 Zerolog 与 Hertz 适配器
	hzotel "github.com/hertz-contrib/obs-opentelemetry/tracing" // 引入 Hertz OpenTelemetry 中间件
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute" // 引入 OTEL 属性包
	"go.opentelemetry.io/otel/codes"     // 引入 OTEL 状态码包
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace" // 引入 OTEL 跟踪包
	"google.golang.org/grpc"
)

var (
	version     = "1.0.0"       //nolint:gochecknoglobals // 定义全局版本号，忽略 linter 警告
	serviceName = "ai-agent-go" //nolint:gochecknoglobals // 定义服务名称，忽略 linter 警告
)

// newOtelResource 创建一个 OpenTelemetry Resource
func newOtelResource(ctx context.Context, serviceName, serviceVersion string) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(serviceVersion),
			semconv.DeploymentEnvironment(os.Getenv("ENV")),
		),
	)
}

// newOtelTracerProvider 创建一个新的 TracerProvider
func newOtelTracerProvider(ctx context.Context, res *resource.Resource, sampler sdktrace.Sampler) (*sdktrace.TracerProvider, error) {
	// 优先从环境变量获取 OTLP exporter 的 endpoint
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		endpoint = "localhost:4317" // 如果环境变量未设置，使用默认值
		glog.Infof("OTEL_EXPORTER_OTLP_ENDPOINT not set, using default: %s", endpoint)
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithInsecure(),
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithDialOption(grpc.WithBlock()),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 OTLP exporter 失败: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	)
	return tp, nil
}

// @title AI Agent API
// @version 1.0
// @description This is the API documentation for the AI Agent service.
// @BasePath /api/v1
func main() {
	initLogger() // 初始化全局日志器

	var configPath string                                                                              // 定义配置文件路径变量
	pflag.StringVarP(&configPath, "config", "c", "internal/config/config.yaml", "Path to config file") // 解析 -c/--config 参数
	pflag.Parse()                                                                                      // 解析命令行参数

	// 加载配置
	cfg, err := config.LoadConfig(configPath) // 从指定路径加载配置
	if err != nil {
		glog.Fatalf("加载配置失败: %v", err) // 加载失败时记录致命日志并退出
	}
	glog.Info("配置加载成功") // 打印配置加载成功信息

	// --- 手动构建 OpenTelemetry Tracer Providers ---
	ctx := context.Background()

	// 1. 为主业务创建 TracerProvider (100% 采样)
	mainResource, err := newOtelResource(ctx, serviceName, version)
	if err != nil {
		glog.Fatalf("创建主业务 Resource 失败: %v", err)
	}
	mainTracerProvider, err := newOtelTracerProvider(ctx, mainResource, sdktrace.AlwaysSample())
	if err != nil {
		glog.Fatalf("创建主业务 TracerProvider 失败: %v", err)
	}
	defer func() {
		if err := mainTracerProvider.Shutdown(context.Background()); err != nil {
			glog.Errorf("主业务 TracerProvider 关闭失败: %v", err)
		}
	}()

	// 2. 为 Relay 创建专用的 TracerProvider (5% 采样)
	relayResource, err := newOtelResource(ctx, "ai-agent-go-relay", version)
	if err != nil {
		glog.Fatalf("创建 Relay Resource 失败: %v", err)
	}
	relayTracerProvider, err := newOtelTracerProvider(ctx, relayResource, sdktrace.TraceIDRatioBased(0.05))
	if err != nil {
		glog.Fatalf("创建 Relay TracerProvider 失败: %v", err)
	}
	defer func() {
		if err := relayTracerProvider.Shutdown(context.Background()); err != nil {
			glog.Errorf("Relay TracerProvider 关闭失败: %v", err)
		}
	}()

	// 3. 设置主业务 TracerProvider 为全局
	otel.SetTracerProvider(mainTracerProvider)
	glog.Info("OpenTelemetry Providers 初始化成功")

	// 使用全局 TracerProvider 创建 Tracer
	appTracer := otel.Tracer("app")

	// 为启动过程创建根 span
	ctx, startupSpan := appTracer.Start(ctx, "Application.Startup",
		trace.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
			attribute.String("config_path", configPath),
		))
	defer startupSpan.End()

	startupSpan.AddEvent("config_loaded")

	// 初始化存储服务
	ctxWithSpan, storageSpan := appTracer.Start(ctx, "storage.init")
	storageManager, err := storage.NewStorage(ctxWithSpan, cfg)
	if err != nil {
		storageSpan.RecordError(err)
		storageSpan.SetStatus(codes.Error, "storage initialization failed")
		storageSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("初始化存储失败: %v", err)
	}
	defer storageManager.Close()
	storageSpan.End()
	ctx = ctxWithSpan
	glog.Info("存储服务初始化成功")
	startupSpan.AddEvent("storage_initialized")

	// 初始化消息中继服务
	ctxWithSpan, relaySpan := appTracer.Start(ctx, "message-relay.init")
	relayLogger := log.New(appCoreLogger.Logger, "[MessageRelay] ", log.LstdFlags|log.Lshortfile)
	messageRelay := outbox.NewMessageRelay(storageManager.MySQL.DB(), storageManager.RabbitMQ, relayLogger)
	relaySpan.End()
	ctx = ctxWithSpan

	// 切换到 Relay Provider 并异步启动 Relay
	otel.SetTracerProvider(relayTracerProvider)
	go func() {
		defer otel.SetTracerProvider(mainTracerProvider) // 确保在 goroutine 退出时切回主 provider
		glog.Info("消息中继服务已启动")
		messageRelay.Start()
	}()
	startupSpan.AddEvent("message_relay_started")

	// ！！！重要：在这里立即切回主业务 Provider，以便后续的初始化（如Embedder）使用正确的追踪器
	otel.SetTracerProvider(mainTracerProvider)

	// 初始化阿里云 Embedder
	ctxWithSpan, embedderSpan := appTracer.Start(ctx, "embedder.init")
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	aliyunEmbedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	if err != nil {
		embedderSpan.RecordError(err)
		embedderSpan.SetStatus(codes.Error, "aliyun embedder initialization failed")
		embedderSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("初始化阿里云Embedder失败: %v", err)
	}
	embedderSpan.End()
	ctx = ctxWithSpan
	glog.Info("阿里云Embedder初始化成功")
	startupSpan.AddEvent("embedder_initialized")

	// 初始化 LLM 聊天模型
	ctxWithSpan, llmSpan := appTracer.Start(ctx, "llm-model.init")
	var llmChatModel model.ToolCallingChatModel
	if cfg.Aliyun.APIKey == "" {
		llmSpan.RecordError(fmt.Errorf("aliyun api key is empty"))
		llmSpan.SetStatus(codes.Error, "aliyun api key not configured")
		llmSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("Aliyun API key is not configured. Cannot initialize LLM model.")
	}

	llmChatModel, err = agent.NewAliyunQwenChatModel(
		cfg.Aliyun.APIKey,
		cfg.GetModelForTask("default"), // Use a default task or a general model
		cfg.Aliyun.APIURL,
	)
	if err != nil {
		llmSpan.RecordError(err)
		llmSpan.SetStatus(codes.Error, "llm model initialization failed")
		llmSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("无法初始化阿里云千问模型: %v", err)
	}
	llmSpan.End()
	ctx = ctxWithSpan
	glog.Info("阿里云千问模型初始化成功")
	startupSpan.AddEvent("llm_model_initialized")

	// 初始化 PDF 提取器
	ctxWithSpan, extractorSpan := appTracer.Start(ctx, "pdf-extractor.init")
	loggerProvider := func(prefix string) *log.Logger {
		return log.New(appCoreLogger.Logger, prefix, log.LstdFlags)
	}
	pdfExtractor, err := processor.BuildPDFExtractor(ctxWithSpan, cfg, loggerProvider)
	if err != nil {
		extractorSpan.RecordError(err)
		extractorSpan.SetStatus(codes.Error, "pdf extractor initialization failed")
		extractorSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("创建PDF提取器失败: %v", err)
	}
	extractorSpan.End()
	ctx = ctxWithSpan
	if cfg.Tika.Type == "tika" && cfg.Tika.ServerURL != "" {
		glog.Info("使用Tika PDF解析器")
	} else {
		glog.Info("使用Eino PDF解析器")
	}
	startupSpan.AddEvent("pdf_extractor_initialized")

	// 创建 Chunker 和 Evaluator 日志器
	var chunkerLogger, evaluatorLogger *log.Logger
	if cfg.Logger.Level == "debug" {
		chunkerLogger = log.New(os.Stderr, "[ChunkerMain] ", log.LstdFlags|log.Lshortfile)
		evaluatorLogger = log.New(os.Stderr, "[EvaluatorMain] ", log.LstdFlags|log.Lshortfile)
	} else {
		chunkerLogger = log.New(io.Discard, "", 0)
		evaluatorLogger = log.New(io.Discard, "", 0)
	}

	// 初始化简历分块器
	ctxWithSpan, chunkerSpan := appTracer.Start(ctx, "resume-chunker.init")
	resumeChunker := parser.NewLLMResumeChunker(llmChatModel, chunkerLogger)
	chunkerSpan.End()
	ctx = ctxWithSpan
	glog.Info("LLM简历分块器初始化成功 (使用默认prompts)")

	// 初始化职位评估器
	ctxWithSpan, evaluatorSpan := appTracer.Start(ctx, "job-evaluator.init")
	jobEvaluator := parser.NewLLMJobEvaluator(llmChatModel, evaluatorLogger)
	evaluatorSpan.End()
	ctx = ctxWithSpan
	glog.Info("LLM JD评估器初始化成功 (使用默认prompts)")
	startupSpan.AddEvent("llm_components_initialized")

	// 初始化简历嵌入器
	ctxWithSpan, resumeEmbedderSpan := appTracer.Start(ctx, "resume-embedder.init")
	defaultResumeEmbedder, err := processor.NewDefaultResumeEmbedder(aliyunEmbedder)
	if err != nil {
		resumeEmbedderSpan.RecordError(err)
		resumeEmbedderSpan.SetStatus(codes.Error, "resume embedder initialization failed")
		resumeEmbedderSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("初始化DefaultResumeEmbedder失败: %v", err)
	}
	resumeEmbedderSpan.End()
	ctx = ctxWithSpan
	glog.Info("DefaultResumeEmbedder初始化成功")
	startupSpan.AddEvent("resume_embedder_initialized")

	// 初始化简历处理器
	ctxWithSpan, processorSpan := appTracer.Start(ctx, "resume-processor.init")
	procComponents := &processor.Components{
		PDFExtractor:   pdfExtractor,
		ResumeChunker:  resumeChunker,
		ResumeEmbedder: defaultResumeEmbedder,
		MatchEvaluator: jobEvaluator,
		Storage:        storageManager,
	}

	processorLogger := log.New(appCoreLogger.Logger, "[ProcessorMain] ", log.LstdFlags|log.Lshortfile)
	procSettings := &processor.Settings{
		UseLLM:            true,
		DefaultDimensions: cfg.Qdrant.Dimension,
		Debug:             cfg.Logger.Level == "debug",
		Logger:            processorLogger,
		TimeLocation:      time.Local,
	}

	resumeProcessor := processor.NewResumeProcessorV2(procComponents, procSettings)
	processorSpan.End()
	ctx = ctxWithSpan
	glog.Info("ResumeProcessor初始化成功")
	startupSpan.AddEvent("resume_processor_initialized")

	// 初始化 JD 处理器
	ctxWithSpan, jdProcessorSpan := appTracer.Start(ctx, "jd-processor.init")
	jdProcessorStdLogger := log.New(appCoreLogger.Logger, "[JDProcessorMain] ", log.LstdFlags|log.Lshortfile)
	jdProcessor, err := processor.NewJDProcessor(aliyunEmbedder, storageManager, cfg.Aliyun.Embedding.Model, processor.WithJDProcessorLogger(jdProcessorStdLogger))
	if err != nil {
		jdProcessorSpan.RecordError(err)
		jdProcessorSpan.SetStatus(codes.Error, "jd processor initialization failed")
		jdProcessorSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("初始化JD处理器失败: %v", err)
	}
	jdProcessorSpan.End()
	ctx = ctxWithSpan
	glog.Info("JD处理器初始化成功")
	startupSpan.AddEvent("jd_processor_initialized")

	// 初始化 Handlers
	ctxWithSpan, handlersSpan := appTracer.Start(ctx, "handlers.init")
	resumeHandler, err := initializeHandler(cfg, storageManager, resumeProcessor)
	if err != nil {
		handlersSpan.RecordError(err)
		handlersSpan.SetStatus(codes.Error, "resume handler initialization failed")
		handlersSpan.End()
		startupSpan.RecordError(err)
		glog.Fatalf("初始化ResumeHandler失败: %v", err)
	}
	glog.Info("ResumeHandler初始化成功")

	jobSearchHandler := handler.NewJobSearchHandler(cfg, storageManager, jdProcessor)
	handlersSpan.End()
	ctx = ctxWithSpan
	glog.Info("JobSearchHandler初始化成功")
	startupSpan.AddEvent("handlers_initialized")

	// 启动后台消费者 goroutine
	go func() {
		uploadConsumerWorkers := 10
		if workers, ok := cfg.RabbitMQ.ConsumerWorkers["upload_consumer_workers"]; ok {
			uploadConsumerWorkers = workers
		}
		batchTimeout := 5 * time.Second
		if timeoutStr, ok := cfg.RabbitMQ.BatchTimeouts["upload_batch_timeout"]; ok {
			d, err := time.ParseDuration(timeoutStr)
			if err == nil {
				batchTimeout = d
			} else {
				glog.Warnf("解析upload_batch_timeout失败 (%s): %v, 使用默认值 %s", timeoutStr, err, batchTimeout)
			}
		}
		glog.Infof("启动上传消费者，工作线程数: %d, 批量超时: %s", uploadConsumerWorkers, batchTimeout)
		err := resumeHandler.StartResumeUploadConsumer(ctx, uploadConsumerWorkers, batchTimeout)
		if err != nil {
			glog.Fatalf("启动简历上传消费者失败: %v", err)
		}

		llmConsumerWorkers := 5
		if workers, ok := cfg.RabbitMQ.ConsumerWorkers["llm_consumer_workers"]; ok {
			llmConsumerWorkers = workers
		}
		glog.Infof("启动LLM解析消费者，工作线程数: %d", llmConsumerWorkers)
		err = resumeHandler.StartLLMParsingConsumer(ctx, llmConsumerWorkers)
		if err != nil {
			glog.Fatalf("启动LLM解析消费者失败: %v", err)
		}

		resumeHandler.StartMD5CleanupTask(ctx)
		glog.Info("所有消费者已启动")
	}()

	// --- 按照 Hertz-contrib/obs-opentelemetry 推荐方式初始化服务器 ---
	tracer, otelCfg := hzotel.NewServerTracer() // 这将从全局（当前是mainTracerProvider）获取配置
	h := server.New(
		server.WithHostPorts(cfg.Server.Address),
		server.WithHandleMethodNotAllowed(true),
		tracer, // 将 tracer 选项传递给服务器
	)

	// ✅ 添加恢复中间件
	h.Use(recovery.Recovery())

	// ✅ 使用官方库返回的配置来初始化中间件
	h.Use(hzotel.ServerMiddleware(otelCfg))

	// 可选：添加访问日志中间件
	h.Use(func(c context.Context, ctx *app.RequestContext) {
		glog.CtxInfof(c, "Request: %s %s", string(ctx.Method()), string(ctx.Path()))
		ctx.Next(c)
		glog.CtxInfof(c, "Response: status %d", ctx.Response.StatusCode())
	})

	// 注册 404 和 405 处理器
	h.NoRoute(func(c context.Context, ctx *app.RequestContext) {
		ctx.JSON(consts.StatusNotFound, map[string]interface{}{
			"error":   "Not Found",
			"message": fmt.Sprintf("The requested path '%s' was not found on this server.", ctx.Path()),
		})
	})
	h.NoMethod(func(c context.Context, ctx *app.RequestContext) {
		ctx.JSON(consts.StatusMethodNotAllowed, map[string]interface{}{
			"error":   "Method Not Allowed",
			"message": fmt.Sprintf("Method '%s' is not allowed for the path '%s'.", ctx.Method(), ctx.Path()),
		})
	})

	router.RegisterRoutes(h, resumeHandler, jobSearchHandler)
	glog.Info("HTTP路由注册成功")

	glog.Infof("HTTP 服务器启动中，监听地址: %s", cfg.Server.Address)

	go func() {
		if err := h.Run(); err != nil {
			glog.Fatalf("启动HTTP服务器失败: %v", err)
		}
	}()

	// 等待系统退出信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	glog.Info("接收到终止信号，正在优雅退出...")

	// 停止消息中继
	messageRelay.Stop()
	glog.Info("消息中继服务已停止")

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := h.Shutdown(shutdownCtx); err != nil {
		glog.Fatalf("服务器关闭失败: %v", err)
	}
	glog.Info("优雅退出完成")
}

// initLogger 初始化全局日志器
func initLogger() {
	logFilePath := "logs/app.log" // 定义日志文件路径

	// 在写入文件前，确保目录存在
	logDir := filepath.Dir(logFilePath)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("创建日志目录失败: %v", err)
	}

	fileWriter, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("无法打开日志文件 %s: %v", logFilePath, err)
	}

	// 配置控制台输出
	consoleWriter := zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: "15:04:05",
	}

	multiWriter := zerolog.MultiLevelWriter(consoleWriter, fileWriter)

	// 设置全局日志级别
	level := zerolog.DebugLevel
	zerolog.SetGlobalLevel(level)

	// 根据环境变量调整时间格式
	if os.Getenv("ENV") == "production" {
		zerolog.TimeFieldFormat = time.RFC3339Nano
	} else {
		zerolog.TimeFieldFormat = "15:04:05"
	}

	// 创建日志实例
	logger := zerolog.New(multiWriter).With().Timestamp().Caller().Logger()

	// 设置全局应用核心日志器和 Zerolog 标准库适配器
	appCoreLogger.Logger = logger
	zlog.Logger = logger

	// 创建 Hertz 日志适配器
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)

	// 设置 Hertz glog
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	log.Println("Logger initialized with Zerolog (appCoreLogger & glog via adapter), writing to console and file:", logFilePath)
}

// initializeHandler 初始化 ResumeHandler
func initializeHandler(cfg *config.Config, storageManager *storage.Storage, resumeProcessor *processor.ResumeProcessor) (*handler.ResumeHandler, error) {
	h := handler.NewResumeHandler(cfg, storageManager, resumeProcessor)
	return h, nil
}
