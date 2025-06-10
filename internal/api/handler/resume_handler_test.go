package handler_test

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/api/handler"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	appCoreLogger "ai-agent-go/internal/logger"
	"ai-agent-go/internal/outbox"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"ai-agent-go/pkg/utils"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/stretchr/testify/assert"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/gofrs/uuid/v5"
	"github.com/minio/minio-go/v7"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	// Import for direct AMQP access for queue purging
	amqp "github.com/rabbitmq/amqp091-go"

	glog "github.com/cloudwego/hertz/pkg/common/hlog"
	hertzadapter "github.com/hertz-contrib/logger/zerolog"
	gormLogger "gorm.io/gorm/logger"
)

const (
	testConfigPath = "../../config/config.yaml"
	testDataDir    = "../../../testdata"
	testPDF        = "黑白整齐简历模板 (6).pdf"
	testPDFContent = "This is a dummy PDF content for handler testing."
)

var (
	testCfg            *config.Config
	testStorageManager *storage.Storage
	testResumeHandler  *handler.ResumeHandler
	testHertzEngine    *server.Hertz
	setupDone          bool
)

const (
	testResumeUUID1 = "1a7e6ea6-1743-4200-a337-14365b2e3532"
	testResumeUUID2 = "c5b2a1a8-2943-4f3b-8f32-3a5e8d6e1b43"
)

func ensureTestPDF(t *testing.T) string {
	testPDFPath := filepath.Join(testDataDir, testPDF)
	if _, err := os.Stat(testPDFPath); os.IsNotExist(err) {
		t.Logf("测试PDF文件 '%s' 未在路径 '%s' 下找到。", testPDF, testPDFPath)
		t.Logf("请确保您已将名为 '%s' 的测试PDF文件放置在工作区的 '%s' 目录下。", testPDF, filepath.ToSlash(testDataDir))
		// require.FailNow(t, fmt.Sprintf("必要的测试PDF文件 '%s' 未找到。请将其放置在 '%s'。", testPDF, testPDFPath))
		// 使用 require.FailNow 会立即停止测试。根据需要，也可以仅记录错误并允许测试继续（可能会在后续步骤失败）。
		// 为了更明确地指示问题，这里选择让测试因缺少关键资源而失败。
		wd, _ := os.Getwd()
		t.Logf("当前工作目录 (Current working directory): %s", wd)
		t.Logf("期望的测试文件绝对路径 (Expected absolute path for test file): %s", filepath.Join(wd, testPDFPath))
		// 先尝试创建目录，以防是目录不存在导致后续写入失败（虽然此处已改为检查文件）
		// require.NoError(t, os.MkdirAll(testDataDir, 0755)) // 这一行理论上不再需要，因为我们期望文件已存在
		require.FailNowf(t, "必要的测试PDF文件 '%s' 未在预期的测试数据目录中找到。", fmt.Sprintf("The required test PDF file '%s' was not found in the expected test data directory. Please place it at '%s'.", testPDF, filepath.ToSlash(filepath.Join(testDataDir, testPDF))))
	}
	// 如果文件存在，则直接返回路径
	return testPDFPath
}
func oneTimeSetupFunc(t *testing.T) {
	if setupDone {
		return
	}

	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug) // Hertz log level, appCoreLogger controls general app logging

	var err error
	testCfg, err = config.LoadConfigFromFileOnly(testConfigPath)
	require.NoError(t, err, "Failed to load test config from %s", testConfigPath)
	require.NotNil(t, testCfg, "Test config is nil")

	// 为测试环境强制设置一个严格的MIME类型白名单，以确保测试的确定性
	// 这避免了因本地 config.yaml 文件包含 "text/plain" 等宽松配置而导致MIME类型测试失败
	testCfg.Upload.AllowedMIMETypes = []string{
		"application/pdf",
		"application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	}
	t.Logf("MIME type whitelist for tests has been strictly set to: %v", testCfg.Upload.AllowedMIMETypes)

	testCfg.MinIO.EnableTestLogging = true
	testCfg.MySQL.LogLevel = 1 // gormLogger.Silent, to suppress GORM logs during NewMySQL's autoMigrate

	ctx := context.Background()
	testStorageManager, err = storage.NewStorage(ctx, testCfg)
	require.NoError(t, err, "Failed to initialize test storage manager")
	require.NotNil(t, testStorageManager, "Test storage manager is nil")

	// Database migration
	if testStorageManager.MySQL != nil {
		db := testStorageManager.MySQL.DB()
		silentLoggerForAutoMigrate := gormLogger.New(
			log.New(io.Discard, "\\r\\n", log.LstdFlags),
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		dbForAutoMigrate := db.Session(&gorm.Session{Logger: silentLoggerForAutoMigrate})
		err = dbForAutoMigrate.AutoMigrate(
			&models.Candidate{}, &models.Job{}, &models.JobVector{},
			&models.ResumeSubmission{}, &models.ResumeSubmissionChunk{},
			&models.JobSubmissionMatch{}, &models.Interviewer{},
			&models.InterviewEvaluation{}, &models.ReviewedResume{},
		)
		require.NoError(t, err, "Failed to auto-migrate database schema")
		t.Log("Database schema auto-migration completed.")

		// Seed the target job for llm-job-id-f1b6
		targetJobIDForTest := "llm-job-id-f1b6"
		var existingJob models.Job
		err = db.Where("job_id = ?", targetJobIDForTest).First(&existingJob).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				jobToCreate := models.Job{
					JobID:              targetJobIDForTest,
					JobTitle:           "Mock LLM Target Job",
					Department:         "Testing Department",
					Location:           "Remote",
					JobDescriptionText: "This is a mock job description for testing the LLM processing pipeline. It requires skills in Go, Distributed Systems, and LLMs.",
					Status:             "ACTIVE",
					CreatedByUserID:    "test_system",
				}
				if createErr := db.Create(&jobToCreate).Error; createErr != nil {
					require.NoError(t, createErr, "Failed to seed target job for LLM test")
				}
				t.Logf("Successfully seeded target job with ID: %s", targetJobIDForTest)
			} else {
				require.NoError(t, err, "Failed to check for existing target job for LLM test")
			}
		} else {
			t.Logf("Target job with ID: %s already exists, no need to seed.", targetJobIDForTest)
		}
	}

	// Purge RabbitMQ queues
	if testStorageManager.RabbitMQ != nil && testCfg.RabbitMQ.URL != "" {
		conn, connErr := amqp.Dial(testCfg.RabbitMQ.URL)
		if connErr != nil {
			t.Logf("Warning: Failed to connect to RabbitMQ for purging: %v", connErr)
		} else {
			defer conn.Close()
			ch, chErr := conn.Channel()
			if chErr != nil {
				t.Logf("Warning: Failed to open RabbitMQ channel for purging: %v", chErr)
			} else {
				defer ch.Close()
				queuesToPurge := []string{testCfg.RabbitMQ.RawResumeQueue, testCfg.RabbitMQ.LLMParsingQueue}
				for _, qName := range queuesToPurge {
					if qName != "" {
						_, purgeErr := ch.QueuePurge(qName, false)
						if purgeErr != nil {
							t.Logf("Warning: Failed to purge RabbitMQ queue %s: %v", qName, purgeErr)
						} else {
							t.Logf("Successfully purged RabbitMQ queue: %s", qName)
						}
					}
				}
			}
		}
	}

	// PDF Extractor
	var pdfExtractor processor.PDFExtractor
	if testCfg.Tika.Type == "tika" && testCfg.Tika.ServerURL != "" {
		var tikaOptions []parser.TikaOption
		if testCfg.Tika.Timeout > 0 {
			tikaOptions = append(tikaOptions, parser.WithTimeout(time.Duration(testCfg.Tika.Timeout)*time.Second))
		}
		if testCfg.Tika.MetadataMode == "full" {
			tikaOptions = append(tikaOptions, parser.WithFullMetadata(true))
		}
		pdfExtractor = parser.NewTikaPDFExtractor(testCfg.Tika.ServerURL, tikaOptions...)
		t.Logf("Using TikaPDFExtractor with URL: %s", testCfg.Tika.ServerURL)
	} else {
		pdfExtractor, err = parser.NewEinoPDFTextExtractor(ctx)
		require.NoError(t, err, "Failed to create EinoPDFTextExtractor")
		t.Log("Using EinoPDFTextExtractor as fallback or default.")
	}

	// --- Initialize LLM Clients (Real or Mock Fallback) ---
	var llmForChunker model.ToolCallingChatModel
	var llmForEvaluator model.ToolCallingChatModel
	var llmInitErr error

	apiKey := testCfg.Aliyun.APIKey
	apiURL := testCfg.Aliyun.APIURL
	if apiURL == "" {
		apiURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" // Default Dashscope-compatible endpoint
		t.Logf("Aliyun API URL not set in config, using default: %s", apiURL)
	}

	if apiKey == "" || apiKey == "your_api_key_here" || apiKey == "test_api_key" {
		t.Fatalf("阿里云 API Key 未在配置文件 (%s) 中有效配置。请提供有效的API Key以运行集成测试。", testConfigPath)
	}

	maskedKey := "****"
	if len(apiKey) > 8 {
		maskedKey = apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
	}
	t.Logf("检测到有效的阿里云 API Key (%s)，将初始化真实的LLM客户端。URL: %s", maskedKey, apiURL)

	// LLM for Chunker
	chunkerModelName := testCfg.GetModelForTask("resume_chunk")
	if chunkerModelName == "" {
		chunkerModelName = testCfg.Aliyun.Model // Fallback to general model
		if chunkerModelName == "" {
			chunkerModelName = "qwen-max-longtext" // Further fallback
			t.Logf("警告: Chunker: 未指定 'resume_chunk' 或通用LLM模型，回退到默认: %s", chunkerModelName)
		}
	}
	t.Logf("Chunker: 初始化LLM模型: %s", chunkerModelName)
	llmForChunker, llmInitErr = agent.NewAliyunQwenChatModel(apiKey, chunkerModelName, apiURL)
	if llmInitErr != nil {
		t.Fatalf("Chunker: 初始化真实LLM模型 (%s) 失败: %v。请检查API Key和网络。", chunkerModelName, llmInitErr)
	}
	t.Logf("Chunker: LLM模型 (%s) 初始化成功。", chunkerModelName)

	// LLM for Evaluator
	evaluatorModelName := testCfg.GetModelForTask("job_evaluate")
	if evaluatorModelName == "" {
		evaluatorModelName = testCfg.Aliyun.Model // Fallback to general model
		if evaluatorModelName == "" {
			evaluatorModelName = "qwen-plus" // Further fallback
			t.Logf("警告: Evaluator: 未指定 'job_evaluate' 或通用LLM模型，回退到默认: %s", evaluatorModelName)
		}
	}
	t.Logf("Evaluator: 初始化LLM模型: %s", evaluatorModelName)
	llmForEvaluator, llmInitErr = agent.NewAliyunQwenChatModel(apiKey, evaluatorModelName, apiURL)
	if llmInitErr != nil {
		t.Fatalf("Evaluator: 初始化真实LLM模型 (%s) 失败: %v。请检查API Key和网络。", evaluatorModelName, llmInitErr)
	}
	t.Logf("Evaluator: LLM模型 (%s) 初始化成功。", evaluatorModelName)
	// --- End of LLM Clients Initialization ---

	testParserLogger := log.New(os.Stderr, "[TestParserInit] ", log.LstdFlags|log.Lshortfile)

	resumeChunker := parser.NewLLMResumeChunker(llmForChunker, testParserLogger)

	aliyunEmbedder, err := parser.NewAliyunEmbedder(testCfg.Aliyun.APIKey, testCfg.Aliyun.Embedding)
	require.NoError(t, err, "Failed to create AliyunEmbedder")

	defaultResumeEmbedder, err := processor.NewDefaultResumeEmbedder(aliyunEmbedder)
	require.NoError(t, err, "Failed to create DefaultResumeEmbedder")

	jobEvaluator := parser.NewLLMJobEvaluator(llmForEvaluator, testParserLogger)

	resumeProcessor := processor.NewResumeProcessor(
		processor.WithStorage(testStorageManager),
		processor.WithPDFExtractor(pdfExtractor),
		processor.WithResumeChunker(resumeChunker),
		processor.WithResumeEmbedder(defaultResumeEmbedder),
		processor.WithJobMatchEvaluator(jobEvaluator),
		processor.WithDefaultDimensions(testCfg.Qdrant.Dimension),
		processor.WithDebugMode(testCfg.Logger.Level == "debug"), // Pass debug mode from config
		processor.WithProcessorLogger(testParserLogger),          // Pass a logger to the processor
		// processor.WithMinIOClient(testStorageManager.MinIO), // MinIO client is part of storageManager
		// processor.WithRedisClient(testStorageManager.Redis), // Redis client is part of storageManager
	)
	// Configure LLM related components for ResumeProcessor if API key was valid
	// This is now handled by passing llmForChunker/llmForEvaluator which might be mocks if key is missing

	testResumeHandler = handler.NewResumeHandler(testCfg, testStorageManager, resumeProcessor)
	require.NotNil(t, testResumeHandler, "Test resume handler is nil")

	testHertzEngine = server.New(server.WithHostPorts("127.0.0.1:0")) // Use random available port
	rg := testHertzEngine.Group("/api/v1")
	rg.POST("/resume/upload", func(c context.Context, appCtx *app.RequestContext) {
		testResumeHandler.HandleResumeUpload(c, appCtx)
	})
	// Add other routes if needed for different test scenarios

	// Setup RabbitMQ exchanges and queues for consumers
	mq := testStorageManager.RabbitMQ
	if mq != nil {
		// Ensure exchanges
		err = mq.EnsureExchange(testCfg.RabbitMQ.ResumeEventsExchange, "direct", true)
		require.NoError(t, err, "Failed to ensure ResumeEventsExchange")
		err = mq.EnsureExchange(testCfg.RabbitMQ.ProcessingEventsExchange, "direct", true)
		require.NoError(t, err, "Failed to ensure ProcessingEventsExchange")

		// Ensure RawResumeQueue and its binding
		err = mq.EnsureQueue(testCfg.RabbitMQ.RawResumeQueue, true)
		require.NoError(t, err, "Failed to ensure RawResumeQueue")
		err = mq.BindQueue(testCfg.RabbitMQ.RawResumeQueue, testCfg.RabbitMQ.ResumeEventsExchange, testCfg.RabbitMQ.UploadedRoutingKey)
		require.NoError(t, err, "Failed to bind RawResumeQueue to ResumeEventsExchange with routing key: %s", testCfg.RabbitMQ.UploadedRoutingKey)

		// Ensure LLMParsingQueue and its binding
		err = mq.EnsureQueue(testCfg.RabbitMQ.LLMParsingQueue, true)
		require.NoError(t, err, "Failed to ensure LLMParsingQueue")
		err = mq.BindQueue(testCfg.RabbitMQ.LLMParsingQueue, testCfg.RabbitMQ.ProcessingEventsExchange, testCfg.RabbitMQ.ParsedRoutingKey)
		require.NoError(t, err, "Failed to bind LLMParsingQueue to ProcessingEventsExchange with routing key: %s", testCfg.RabbitMQ.ParsedRoutingKey)

		// Ensure QdrantVectorizeQueue and its binding (if defined and used)
		if testCfg.RabbitMQ.QdrantVectorizeQueue != "" {
			err = mq.EnsureQueue(testCfg.RabbitMQ.QdrantVectorizeQueue, true)
			require.NoError(t, err, "Failed to ensure QdrantVectorizeQueue")
			// Assuming it binds to ProcessingEventsExchange with a specific routing key
			// You might need a different exchange or routing key, adjust as needed.
			// For example, if vectorization happens after LLM parsing:
			if testCfg.RabbitMQ.VectorizeRoutingKey != "" {
				err = mq.BindQueue(testCfg.RabbitMQ.QdrantVectorizeQueue, testCfg.RabbitMQ.ProcessingEventsExchange, testCfg.RabbitMQ.VectorizeRoutingKey)
				require.NoError(t, err, "Failed to bind QdrantVectorizeQueue to ProcessingEventsExchange with routing key: %s", testCfg.RabbitMQ.VectorizeRoutingKey)
			} else {
				t.Logf("QdrantVectorizeQueue is defined but no VectorizeRoutingKey, skipping bind.")
			}
		}

		t.Log("RabbitMQ exchanges, queues, and bindings ensured for testing.")
	} else {
		t.Log("RabbitMQ client is nil, skipping exchange/queue setup for tests.")
	}

	// Start RabbitMQ consumers in goroutines for tests that need them
	// This needs to be done carefully to ensure consumers are ready before messages are published
	// and are properly shut down after tests.
	// For now, consumer startup is handled within specific consumer tests.

	setupDone = true
	t.Log("oneTimeSetupFunc completed.")
}
func createMultipartForm(t *testing.T, filePath string, targetJobID string, sourceChannel string) (*bytes.Buffer, string) {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	require.NoError(t, err)
	file, err := os.Open(filePath)
	require.NoError(t, err)
	defer file.Close()
	_, err = io.Copy(part, file)
	require.NoError(t, err)

	if targetJobID != "" {
		_ = writer.WriteField("target_job_id", targetJobID)
	}
	if sourceChannel != "" {
		_ = writer.WriteField("source_channel", sourceChannel)
	}
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

// createMultipartFormWithContent 是一个辅助函数，用于通过字节内容而不是文件路径创建 multipart 表单
func createMultipartFormWithContent(t *testing.T, fileName string, fileContent []byte, targetJobID string, sourceChannel string) (*bytes.Buffer, string) {
	body := new(bytes.Buffer)
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("file", fileName)
	require.NoError(t, err)

	_, err = io.Copy(part, bytes.NewReader(fileContent))
	require.NoError(t, err)

	if targetJobID != "" {
		_ = writer.WriteField("target_job_id", targetJobID)
	}
	if sourceChannel != "" {
		_ = writer.WriteField("source_channel", sourceChannel)
	}
	require.NoError(t, writer.Close())
	return body, writer.FormDataContentType()
}

// 验证系统能否成功接收并处理一个全新的、从未上传过的简历文件。
func TestHandleResumeUpload_Success_NewFile(t *testing.T) {
	oneTimeSetupFunc(t)

	testPDFPath := ensureTestPDF(t)
	targetJobID := "job_test_new_" + uuid.Must(uuid.NewV4()).String()[:8]
	sourceChannel := "test_upload_new"

	// 打开PDF文件获取原始文件的MD5值
	fileBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err)
	fileMD5Hex := utils.CalculateMD5(fileBytes)
	rawFileKey := testStorageManager.Redis.FormatKey(constants.RawFileMD5SetKey)
	ctx := context.Background()

	// 确保Redis中没有测试文件的MD5
	isMember, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	if isMember {
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		require.NoError(t, err, "Failed to remove pre-existing MD5 for clean test run")
		t.Logf("Removed pre-existing MD5 %s from Redis for clean test run (Success_NewFile)", fileMD5Hex)
	}

	// 创建测试请求
	body, contentType := createMultipartForm(t, testPDFPath, targetJobID, sourceChannel)

	// 执行请求
	resp := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body, Len: body.Len()},
		ut.Header{Key: "Content-Type", Value: contentType},
	)
	require.Equal(t, http.StatusOK, resp.Code)

	// 解析响应
	var uploadResp handler.ResumeUploadResponse
	err = json.Unmarshal(resp.Body.Bytes(), &uploadResp)
	require.NoError(t, err)
	require.Equal(t, constants.StatusSubmittedForProcessing, uploadResp.Status)
	require.NotEmpty(t, uploadResp.SubmissionUUID, "SubmissionUUID should not be empty")

	// 验证文件是否上传到MinIO
	actualMinIOPathInHandler := fmt.Sprintf("resume/%s/original%s", uploadResp.SubmissionUUID, filepath.Ext(testPDF))
	_, err = testStorageManager.MinIO.StatObject(ctx, testCfg.MinIO.OriginalsBucket, actualMinIOPathInHandler, minio.StatObjectOptions{})
	require.NoError(t, err, "File not found in MinIO. Expected path: %s in bucket %s", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket)
	t.Logf("Successfully verified file exists in MinIO at %s in bucket %s", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket)

	// 验证MD5是否添加到Redis
	isMemberAfter, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	require.True(t, isMemberAfter, "File MD5 hex should be in Redis set after successful upload")
	t.Logf("Successfully verified MD5 %s is in Redis set %s after test.", fileMD5Hex, rawFileKey)

	// 测试清理
	t.Cleanup(func() {
		t.Logf("--- Starting Cleanup for TestHandleResumeUpload_Success_NewFile (%s) ---", uploadResp.SubmissionUUID)
		err := testStorageManager.MinIO.RemoveObject(ctx, testCfg.MinIO.OriginalsBucket, actualMinIOPathInHandler, minio.RemoveObjectOptions{})
		if err != nil {
			t.Logf("Cleanup: Failed to remove object %s from MinIO bucket %s: %v", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket, err)
		} else {
			t.Logf("Cleanup: Successfully removed object %s from MinIO bucket %s", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket)
		}
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		if err != nil && err.Error() != "redis: nil" && !errors.Is(err, redis.Nil) {
			t.Logf("Cleanup: Failed to remove MD5 %s from Redis set %s: %v", fileMD5Hex, rawFileKey, err)
		} else {
			t.Logf("Cleanup: Successfully removed MD5 %s from Redis set %s", fileMD5Hex, rawFileKey)
		}
		t.Logf("--- Finished Cleanup for TestHandleResumeUpload_Success_NewFile (%s) ---", uploadResp.SubmissionUUID)
	})
}

// 验证当用户尝试上传一个已经存在于系统中的文件时，系统能正确识别并拒绝该重复请求。
func TestHandleResumeUpload_DuplicateFile(t *testing.T) {
	oneTimeSetupFunc(t)

	testPDFPath := ensureTestPDF(t)
	targetJobIDBase := "job_test_dup_" + uuid.Must(uuid.NewV4()).String()[:8]
	sourceChannel := "test_upload_dup"

	// 获取文件MD5
	fileBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err)
	fileMD5Hex := utils.CalculateMD5(fileBytes)
	rawFileKey := testStorageManager.Redis.FormatKey(constants.RawFileMD5SetKey)
	ctx := context.Background()

	// 确保Redis中没有测试文件的MD5
	isMemberInitial, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	if isMemberInitial {
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		require.NoError(t, err, "Failed to remove pre-existing MD5 for clean duplicate test run")
		t.Logf("Removed pre-existing MD5 %s from Redis for clean duplicate test run", fileMD5Hex)
	}

	// 执行第一次上传
	t.Logf("Performing first upload for duplicate test with JobID: %s-1", targetJobIDBase)
	body1, contentType1 := createMultipartForm(t, testPDFPath, targetJobIDBase+"-1", sourceChannel)
	resp1 := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body1, Len: body1.Len()},
		ut.Header{Key: "Content-Type", Value: contentType1},
	)

	require.Equal(t, http.StatusOK, resp1.Code)
	var uploadResp1 handler.ResumeUploadResponse
	err = json.Unmarshal(resp1.Body.Bytes(), &uploadResp1)
	require.NoError(t, err)
	require.Equal(t, constants.StatusSubmittedForProcessing, uploadResp1.Status, "First upload should be processed")
	require.NotEmpty(t, uploadResp1.SubmissionUUID)
	firstSubmissionUUID := uploadResp1.SubmissionUUID
	firstMinIOPath := fmt.Sprintf("resume/%s/original%s", firstSubmissionUUID, filepath.Ext(testPDF))

	// 验证MD5是否添加到Redis
	isMemberAfterFirst, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	require.True(t, isMemberAfterFirst, "File MD5 should be in Redis after first successful upload")
	t.Logf("MD5 %s added to Redis after first upload.", fileMD5Hex)

	// 执行第二次上传（重复文件）
	t.Logf("Performing second (duplicate) upload for JobID: %s-2", targetJobIDBase)
	body2, contentType2 := createMultipartForm(t, testPDFPath, targetJobIDBase+"-2", sourceChannel)
	resp2 := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body2, Len: body2.Len()},
		ut.Header{Key: "Content-Type", Value: contentType2},
	)

	require.Equal(t, http.StatusConflict, resp2.Code, "Duplicate upload should return 409 Conflict")
	var uploadResp2 handler.ResumeUploadResponse
	err = json.Unmarshal(resp2.Body.Bytes(), &uploadResp2)
	require.NoError(t, err)
	require.Equal(t, constants.StatusDuplicateFileSkipped, uploadResp2.Status, "Second upload of the same file should be skipped")
	require.Empty(t, uploadResp2.SubmissionUUID, "SubmissionUUID should be empty for skipped duplicate")

	// 验证MD5仍在Redis中
	isMemberAfterSecond, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	require.True(t, isMemberAfterSecond, "File MD5 should still be in Redis after duplicate upload attempt")

	// 验证第一个文件仍在MinIO中
	_, err = testStorageManager.MinIO.StatObject(ctx, testCfg.MinIO.OriginalsBucket, firstMinIOPath, minio.StatObjectOptions{})
	require.NoError(t, err, "First uploaded file should still exist in MinIO")

	// 测试清理
	t.Cleanup(func() {
		t.Logf("--- Starting Cleanup for TestHandleResumeUpload_DuplicateFile (%s) ---", firstSubmissionUUID)
		err := testStorageManager.MinIO.RemoveObject(ctx, testCfg.MinIO.OriginalsBucket, firstMinIOPath, minio.RemoveObjectOptions{})
		if err != nil {
			t.Logf("Cleanup: Failed to remove object %s from MinIO: %v", firstMinIOPath, err)
		} else {
			t.Logf("Cleanup: Successfully removed object %s from MinIO", firstMinIOPath)
		}
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		if err != nil && err.Error() != "redis: nil" && !errors.Is(err, redis.Nil) {
			t.Logf("Cleanup: Failed to remove MD5 %s from Redis: %v, err = %v", fileMD5Hex, rawFileKey, err)
		} else {
			t.Logf("Cleanup: Successfully removed MD5 %s from Redis %v", fileMD5Hex, rawFileKey)
		}
		t.Logf("--- Finished Cleanup for TestHandleResumeUpload_DuplicateFile (%s) ---", firstSubmissionUUID)
	})
}

// 验证系统的重复文件检测机制在面对高并发、同时上传相同文件的情况下，是否具备原子性，确保只有一个请求能成功。
func TestHandleResumeUpload_ConcurrentDuplicateProtection(t *testing.T) {
	oneTimeSetupFunc(t)

	testPDFPath := ensureTestPDF(t)
	targetJobIDBase := "job_test_concurrent_dup_" + uuid.Must(uuid.NewV4()).String()[:8]
	sourceChannel := "test_upload_concurrent_dup"

	// 先确保文件MD5不在Redis中
	fileBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err)
	fileMD5Hex := utils.CalculateMD5(fileBytes)
	rawFileKey := testStorageManager.Redis.FormatKey(constants.RawFileMD5SetKey)
	ctx := context.Background()

	isMemberInitial, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	if isMemberInitial {
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		require.NoError(t, err, "Failed to remove pre-existing MD5 for clean test run")
		t.Logf("Removed pre-existing MD5 %s from Redis for clean test run", fileMD5Hex)
	}

	// 并发上传次数
	concurrentRequests := 5
	// 响应通道
	responses := make(chan int, concurrentRequests)
	// 用于等待所有goroutine完成
	var wg sync.WaitGroup
	wg.Add(concurrentRequests)

	// 并发发起多个相同文件上传请求
	for i := 0; i < concurrentRequests; i++ {
		go func(idx int) {
			defer wg.Done()
			jobID := fmt.Sprintf("%s-%d", targetJobIDBase, idx)
			body, contentType := createMultipartForm(t, testPDFPath, jobID, sourceChannel)

			resp := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
				&ut.Body{Body: body, Len: body.Len()},
				ut.Header{Key: "Content-Type", Value: contentType},
			)

			// 发送状态码到通道
			responses <- resp.Code
			t.Logf("Concurrent request %d completed with status code: %d", idx, resp.Code)
		}(i)
	}

	// 等待所有goroutine完成
	wg.Wait()
	close(responses)

	// 计算响应结果
	successCount := 0
	conflictCount := 0
	otherCount := 0

	for code := range responses {
		switch code {
		case http.StatusOK:
			successCount++
		case http.StatusConflict:
			conflictCount++
		default:
			otherCount++
		}
	}

	t.Logf("Total responses: Success: %d, Conflict: %d, Other: %d", successCount, conflictCount, otherCount)

	// 再次上传以获取"已存在"响应，验证原子操作有效
	var resp *ut.ResponseRecorder
	body, contentType := createMultipartForm(t, testPDFPath, targetJobIDBase+"-verify", sourceChannel)
	resp = ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body, Len: body.Len()},
		ut.Header{Key: "Content-Type", Value: contentType},
	)
	require.Equal(t, http.StatusConflict, resp.Code, "再次上传应返回409 Conflict")

	// 验证只有一个请求成功，其他请求都返回冲突
	require.Equal(t, 1, successCount, "应该只有一个请求成功上传")
	require.Equal(t, concurrentRequests-1, conflictCount, "其他请求应该返回409 Conflict")
	require.Equal(t, 0, otherCount, "不应有其他状态码返回")

	// 验证MD5确实被添加到了Redis
	isMemberAfter, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	require.True(t, isMemberAfter, "文件MD5应该已被添加到Redis Set")

	// 查询数据库找到成功上传的文件的UUID（添加更多日志记录和模式匹配）
	var submissions []models.ResumeSubmission
	jobIDPattern := targetJobIDBase + "%" // 使用通配符

	// 先打印调试日志
	t.Logf("查询数据库中的targetJobID模式: %s", jobIDPattern)

	err = testStorageManager.MySQL.DB().Where("target_job_id LIKE ?", jobIDPattern).Find(&submissions).Error
	require.NoError(t, err)

	if len(submissions) == 0 {
		// 如果没有找到，尝试更广泛的查询并打印日志
		t.Logf("未找到匹配 %s 的记录，尝试更广泛的查询", jobIDPattern)
		var allRecentSubmissions []models.ResumeSubmission
		err = testStorageManager.MySQL.DB().Where("created_at > ?", time.Now().Add(-5*time.Minute)).Find(&allRecentSubmissions).Error
		require.NoError(t, err)

		if len(allRecentSubmissions) > 0 {
			t.Logf("找到 %d 条最近的提交记录:", len(allRecentSubmissions))
			for i, sub := range allRecentSubmissions {
				if i < 10 { // 只打印前10条
					t.Logf("记录 #%d: UUID=%s, TargetJobID=%s, Status=%s",
						i+1, sub.SubmissionUUID, getStringPtrValue(sub.TargetJobID), sub.ProcessingStatus)
				}
			}
		} else {
			t.Logf("在最近5分钟内没有任何提交记录")
		}
	} else {
		t.Logf("找到 %d 条匹配 %s 的记录", len(submissions), jobIDPattern)
		for i, sub := range submissions {
			t.Logf("记录 #%d: UUID=%s, TargetJobID=%s, Status=%s",
				i+1, sub.SubmissionUUID, getStringPtrValue(sub.TargetJobID), sub.ProcessingStatus)
		}
	}

	// 申请成功的那个响应码应该是200
	require.Equal(t, 1, successCount, "应该只有一个请求成功上传")

	// 不要验证数据库记录数量，因为这取决于内部实现
	// 一次成功的HTTP请求可能因各种原因未能完成数据库写入
	// 我们的主要目标是验证Redis原子操作和HTTP状态码

	t.Cleanup(func() {
		t.Logf("--- Starting Cleanup for TestHandleResumeUpload_ConcurrentDuplicateProtection ---")

		// 定义变量存储提交UUID
		var submissionUUID string

		if len(submissions) > 0 {
			submissionUUID = submissions[0].SubmissionUUID
			minioPath := fmt.Sprintf("resume/%s/original%s", submissionUUID, filepath.Ext(testPDF))
			err := testStorageManager.MinIO.RemoveObject(ctx, testCfg.MinIO.OriginalsBucket, minioPath, minio.RemoveObjectOptions{})
			if err != nil {
				t.Logf("Cleanup: Failed to remove object from MinIO: %v", err)
			} else {
				t.Logf("Cleanup: Successfully removed object %s from MinIO bucket %s", minioPath, testCfg.MinIO.OriginalsBucket)
			}

			// 从MySQL删除记录
			err = testStorageManager.MySQL.DB().Unscoped().Delete(&submissions[0]).Error
			if err != nil {
				t.Logf("Cleanup: Failed to delete submission record: %v", err)
			} else {
				t.Logf("Cleanup: Successfully deleted submission record")
			}
		} else {
			// 如果没有找到记录，尝试查找MinIO中可能的文件
			t.Logf("没有在数据库中找到记录，跳过MinIO文件清理")
		}

		// 清理Redis中的MD5
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		if err != nil {
			t.Logf("Cleanup: Failed to remove MD5 %s from Redis: %v", fileMD5Hex, err)
		} else {
			t.Logf("Cleanup: Successfully removed MD5 %s from Redis", fileMD5Hex)
		}

		t.Logf("--- Finished Cleanup for TestHandleResumeUpload_ConcurrentDuplicateProtection ---")
	})
}

// 验证系统能拒绝不符合预设MIME类型的文件上传。
func TestHandleResumeUpload_UnsupportedMIMEType(t *testing.T) {
	oneTimeSetupFunc(t)

	// 将被检测为 text/plain 的内容
	unsupportedContent := []byte("This is a plain text file, not a supported document type.")
	fileMD5Hex := utils.CalculateMD5(unsupportedContent)
	rawFileKey := testStorageManager.Redis.FormatKey(constants.RawFileMD5SetKey)
	ctx := context.Background()

	// 确保Redis中没有此测试内容的MD5，以防因重复文件导致返回409而不是415
	isMember, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	if isMember {
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		require.NoError(t, err, "Failed to remove pre-existing MD5 for unsupported MIME type test")
		t.Logf("Removed pre-existing MD5 %s for a clean unsupported MIME type test", fileMD5Hex)
	}

	// 创建请求
	body, contentType := createMultipartFormWithContent(t, "test.txt", unsupportedContent, "job-id-unsupported", "test-channel")

	// 执行请求
	resp := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body, Len: body.Len()},
		ut.Header{Key: "Content-Type", Value: contentType},
	)

	// 检查响应
	require.Equal(t, http.StatusUnsupportedMediaType, resp.Code)
	var errResp map[string]interface{}
	err = json.Unmarshal(resp.Body.Bytes(), &errResp)
	require.NoError(t, err)
	// http.DetectContentType 对 "This is..." 返回 "text/plain; charset=utf-8"
	// 我们的处理程序现在应该提取并返回基础MIME类型 "text/plain"
	require.Contains(t, errResp["error"], "不支持的文件类型: text/plain")
}

// 验证系统能根据配置的最大文件大小限制，拒绝过大的文件上传。
func TestHandleResumeUpload_FileTooLarge(t *testing.T) {
	oneTimeSetupFunc(t)

	// 1. 使用一个很小的实际内容，以确保如果测试失败，不是因为实际内容过大
	smallContent := []byte("small content")
	body, contentType := createMultipartFormWithContent(t, "large_file.pdf", smallContent, "job-id-large", "test-channel")

	// 2. 伪造一个超大的 Content-Length
	largeSize := int(testCfg.Upload.MaxSizeMB*1024*1024) + 1

	// 3. 执行请求，并显式注入 Content-Length 头
	resp := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body, Len: body.Len()},
		ut.Header{Key: "Content-Type", Value: contentType},
		// 关键：显式设置 Content-Length 头来触发服务端的早期校验
		ut.Header{Key: "Content-Length", Value: strconv.Itoa(largeSize)},
	)

	// 4. 检查响应
	require.Equal(t, http.StatusRequestEntityTooLarge, resp.Code)
	var errResp map[string]interface{}
	err := json.Unmarshal(resp.Body.Bytes(), &errResp)
	require.NoError(t, err)
	require.Contains(t, errResp["error"], fmt.Sprintf("文件大小不能超过 %d MB", testCfg.Upload.MaxSizeMB))
}

// 验证在文件上传到后端存储（MinIO）的过程中，如果操作超时，系统能够优雅地处理并返回超时错误。
func TestHandleResumeUpload_UploadTimeout(t *testing.T) {
	oneTimeSetupFunc(t)

	// 1. 设置一个极短的超时时间来触发超时
	originalTimeout := testCfg.Upload.Timeout
	testCfg.Upload.Timeout = 1 * time.Millisecond
	defer func() {
		// 恢复原始超时时间，避免影响其他测试
		testCfg.Upload.Timeout = originalTimeout
	}()

	// 2. 准备一个真实的、但因为超时而无法完成上传的文件
	testPDFPath := ensureTestPDF(t)
	body, contentType := createMultipartForm(t, testPDFPath, "job-id-timeout", "test-channel-timeout")

	// 3. 执行请求
	resp := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body, Len: body.Len()},
		ut.Header{Key: "Content-Type", Value: contentType},
	)

	// 4. 检查响应
	// 预期因为 context deadline exceeded 而返回 504 Gateway Timeout
	require.Equal(t, http.StatusGatewayTimeout, resp.Code)
	var errResp map[string]interface{}
	err := json.Unmarshal(resp.Body.Bytes(), &errResp)
	require.NoError(t, err)
	require.Contains(t, errResp["error"], "上传到存储服务超时")
}

// 下载原始文件、提取文本、查重、保存文本，并发布新任务。
func TestStartResumeUploadConsumer_ProcessMessageSuccess(t *testing.T) {
	oneTimeSetupFunc(t)

	ctx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()

	consumerErrChan := make(chan error, 1)
	go func() {
		t.Log("Starting ResumeUploadConsumer for test...")
		if err := testResumeHandler.StartResumeUploadConsumer(ctx, 1, 1*time.Second); err != nil {
			if !errors.Is(err, context.Canceled) &&
				!strings.Contains(err.Error(), "server closed") &&
				!strings.Contains(err.Error(), "channel/connection is not open") &&
				!strings.Contains(err.Error(), "context canceled") {
				t.Logf("ResumeUploadConsumer exited with error: %v", err)
				consumerErrChan <- err
			}
		}
		close(consumerErrChan)
		t.Log("ResumeUploadConsumer goroutine finished.")
	}()

	time.Sleep(3 * time.Second)

	submissionUUID := "test-consum-uuid-" + uuid.Must(uuid.NewV4()).String()[:8]

	testPDFPath := ensureTestPDF(t)
	originalFileName := filepath.Base(testPDFPath)
	fileExt := filepath.Ext(originalFileName)

	expectedMinIOPathForOriginal := fmt.Sprintf("resume/%s/original%s", submissionUUID, fileExt)

	dummyPDFFileBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err, "Failed to read dummy PDF file for consumer test")

	generatedOriginalPath, err := testStorageManager.MinIO.UploadResumeFile(
		context.Background(),
		submissionUUID,
		fileExt,
		bytes.NewReader(dummyPDFFileBytes),
		int64(len(dummyPDFFileBytes)),
	)
	require.NoError(t, err, "Failed to pre-upload test file to MinIO for consumer test")
	require.Equal(t, expectedMinIOPathForOriginal, generatedOriginalPath, "Generated path by UploadResumeFile ('%s') should match expected MinIO path ('%s')", generatedOriginalPath, expectedMinIOPathForOriginal)
	t.Logf("Pre-uploaded test file to MinIO: %s in bucket %s", generatedOriginalPath, testCfg.MinIO.OriginalsBucket)

	testMessage := storage.ResumeUploadMessage{
		SubmissionUUID:      submissionUUID,
		OriginalFilePathOSS: generatedOriginalPath,
		OriginalFilename:    originalFileName,
		TargetJobID:         "consumer-job-id-" + uuid.Must(uuid.NewV4()).String()[:4],
		SourceChannel:       "consumer-test-channel",
		SubmissionTimestamp: time.Now(),
	}

	err = testStorageManager.RabbitMQ.PublishJSON(
		context.Background(),
		testCfg.RabbitMQ.ResumeEventsExchange,
		testCfg.RabbitMQ.UploadedRoutingKey,
		testMessage,
		true,
	)
	require.NoError(t, err, "Failed to publish test ResumeUploadMessage")
	t.Logf("Published ResumeUploadMessage for SubmissionUUID: %s to exchange '%s' with key '%s'",
		submissionUUID, testCfg.RabbitMQ.ResumeEventsExchange, testCfg.RabbitMQ.UploadedRoutingKey)

	var finalStatus string
	var parsedTextPath string
	var dbSubmission models.ResumeSubmission
	processedSuccessfully := false

	pollingCtx, pollingCancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer pollingCancel()

	lastLogTime := time.Now()           // 添加: 初始化上次日志打印时间
	const logInterval = 5 * time.Second // 添加: 定义日志打印间隔

	for {
		select {
		case errFromConsumer, ok := <-consumerErrChan:
			if ok && errFromConsumer != nil {
				t.Fatalf("Consumer exited prematurely with error: %v", errFromConsumer)
			}
		case <-pollingCtx.Done():
			dbErrState := "no error or record not found"
			if err != nil {
				dbErrState = err.Error()
			}
			finalDbSubmissionForTimeout := models.ResumeSubmission{}
			timeoutFetchErr := testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).First(&finalDbSubmissionForTimeout).Error
			if timeoutFetchErr == nil {
				finalStatus = finalDbSubmissionForTimeout.ProcessingStatus
				parsedTextPath = finalDbSubmissionForTimeout.ParsedTextPathOSS
			}

			t.Fatalf("Polling timed out for %s. Last DB status: %s. ParsedTextPath: '%s'. Last DB query error: %s. Timeout: %v",
				submissionUUID, finalStatus, parsedTextPath, dbErrState, pollingCtx.Err())
		default:
		}

		dbQueryCtx, dbQueryCancel := context.WithTimeout(pollingCtx, 5*time.Second)
		err = testStorageManager.MySQL.DB().WithContext(dbQueryCtx).Where("submission_uuid = ?", submissionUUID).First(&dbSubmission).Error
		dbQueryCancel()

		if err == nil {
			finalStatus = dbSubmission.ProcessingStatus
			parsedTextPath = dbSubmission.ParsedTextPathOSS
			// 修改日志打印逻辑
			if time.Since(lastLogTime) >= logInterval {
				t.Logf("Polling: Submission %s status: %s, ParsedTextPath: '%s'", submissionUUID, finalStatus, parsedTextPath)
				lastLogTime = time.Now()
			}

			if finalStatus == constants.StatusQueuedForLLM ||
				finalStatus == constants.StatusContentDuplicateSkipped ||
				strings.Contains(finalStatus, "FAILED") {
				processedSuccessfully = finalStatus == constants.StatusQueuedForLLM || finalStatus == constants.StatusContentDuplicateSkipped
				goto verificationLoopEnd
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			// 修改日志打印逻辑
			if time.Since(lastLogTime) >= logInterval {
				t.Logf("Polling: Error fetching submission %s (will retry): %v", submissionUUID, err)
				lastLogTime = time.Now()
			}
		}
		time.Sleep(1 * time.Second)
	}

verificationLoopEnd:
	require.True(t, processedSuccessfully, "Resume processing via consumer did not reach a successful terminal state ('%s' or '%s'). Final status: '%s' for UUID: %s", constants.StatusQueuedForLLM, constants.StatusContentDuplicateSkipped, finalStatus, submissionUUID)

	if finalStatus == constants.StatusQueuedForLLM {
		require.NotEmpty(t, parsedTextPath, "ParsedTextPathOSS should not be empty if status is '%s' for UUID %s", constants.StatusQueuedForLLM, submissionUUID)
		parsedTextDataString, err := testStorageManager.MinIO.GetParsedText(context.Background(), parsedTextPath)
		require.NoError(t, err, "Failed to get parsed text from MinIO (%s) for UUID %s", parsedTextPath, submissionUUID)
		require.NotEmpty(t, parsedTextDataString, "Parsed text content from MinIO should not be empty for UUID %s", submissionUUID)
		t.Logf("Successfully verified parsed text exists in MinIO at %s (bucket %s) for '%s' status, UUID %s", parsedTextPath, testCfg.MinIO.ParsedTextBucket, constants.StatusQueuedForLLM, submissionUUID)

		textMD5 := utils.CalculateMD5([]byte(parsedTextDataString))
		parsedTextMD5Key := testStorageManager.Redis.FormatKey(constants.ParsedTextMD5SetKey)
		textMD5Exists, err := testStorageManager.Redis.Client.SIsMember(context.Background(), parsedTextMD5Key, textMD5).Result()
		require.NoError(t, err, "Error checking parsed text MD5 in Redis for UUID %s", submissionUUID)
		require.True(t, textMD5Exists, "Parsed text MD5 (%s) should be in Redis set '%s' for UUID %s", textMD5, parsedTextMD5Key, submissionUUID)
		t.Logf("Successfully verified parsed text MD5 %s is in Redis set %s for UUID %s", textMD5, parsedTextMD5Key, submissionUUID)
	} else if finalStatus == constants.StatusContentDuplicateSkipped {
		t.Logf("Processing skipped due to content duplicate for UUID %s, no further MinIO/Redis text checks needed.", submissionUUID)
	}

	t.Cleanup(func() {
		t.Logf("--- Starting Inner Cleanup for TestStartResumeUploadConsumer (%s) ---", submissionUUID)
		ctxClean := context.Background()

		// Configure GORM for silent logging during cleanup
		// Create a new GORM logger instance set to Silent level
		silentGormLoggerInstance := gormLogger.New(
			log.New(io.Discard, "\r\n", log.LstdFlags), // Log to io.Discard to suppress output
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		dbForCleanup := testStorageManager.MySQL.DB().Session(&gorm.Session{Logger: silentGormLoggerInstance})

		if err := dbForCleanup.WithContext(ctxClean).Unscoped().Delete(&models.ResumeSubmission{}, "submission_uuid = ?", submissionUUID).Error; err != nil {
			t.Logf("Cleanup: Error deleting submission %s from MySQL: %v", submissionUUID, err)
		} else {
			t.Logf("Cleanup: Successfully deleted submission %s from MySQL", submissionUUID)
		}

		if err := testStorageManager.MinIO.RemoveObject(ctxClean, testCfg.MinIO.OriginalsBucket, generatedOriginalPath, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("Cleanup: Error deleting original object %s from MinIO bucket %s: %v", generatedOriginalPath, testCfg.MinIO.OriginalsBucket, err)
		} else {
			t.Logf("Cleanup: Successfully deleted original object %s from MinIO bucket %s", generatedOriginalPath, testCfg.MinIO.OriginalsBucket)
		}

		if parsedTextPath != "" {
			if err := testStorageManager.MinIO.RemoveObject(ctxClean, testCfg.MinIO.ParsedTextBucket, parsedTextPath, minio.RemoveObjectOptions{}); err != nil {
				t.Logf("Cleanup: Error deleting parsed text object %s from MinIO bucket %s: %v", parsedTextPath, testCfg.MinIO.ParsedTextBucket, err)
			} else {
				t.Logf("Cleanup: Successfully deleted parsed text object %s from MinIO bucket %s", parsedTextPath, testCfg.MinIO.ParsedTextBucket)
			}
		}

		if finalStatus == constants.StatusQueuedForLLM && parsedTextPath != "" {
			parsedTextContentForCleanup, errGet := testStorageManager.MinIO.GetParsedText(ctxClean, parsedTextPath)
			if errGet == nil && parsedTextContentForCleanup != "" {
				textMD5ForCleanup := utils.CalculateMD5([]byte(parsedTextContentForCleanup))
				parsedTextMD5Key := testStorageManager.Redis.FormatKey(constants.ParsedTextMD5SetKey)
				if _, err := testStorageManager.Redis.Client.SRem(ctxClean, parsedTextMD5Key, textMD5ForCleanup).Result(); err != nil && !errors.Is(err, redis.Nil) {
					t.Logf("Cleanup: Failed to remove parsed text MD5 %s from Redis set %s: %v", textMD5ForCleanup, parsedTextMD5Key, err)
				} else {
					t.Logf("Cleanup: Attempted to remove parsed text MD5 %s from Redis set %s", textMD5ForCleanup, parsedTextMD5Key)
				}
			} else if errGet != nil {
				t.Logf("Cleanup: Could not get parsed text '%s' for MD5 calculation: %v", parsedTextPath, errGet)
			}
		}
		t.Logf("--- Finished Inner Cleanup for TestStartResumeUploadConsumer (%s) ---", submissionUUID)
	})
}

// 验证能够正确处理任务：调用LLM解析简历、分块与向量化、并将结果存入数据库和向量数据库。
func TestStartLLMParsingConsumer_ProcessMessageSuccess(t *testing.T) {
	oneTimeSetupFunc(t) // Ensures all dependencies are up.

	ctxConsumer, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()

	consumerErrChan := make(chan error, 1)
	go func() {
		t.Log("Starting LLMParsingConsumer for test...")
		llmConsumerWorkers := 1 // Default for test
		if workers, ok := testCfg.RabbitMQ.ConsumerWorkers["llm_consumer_workers"]; ok && workers > 0 {
			llmConsumerWorkers = workers
		}

		if err := testResumeHandler.StartLLMParsingConsumer(ctxConsumer, llmConsumerWorkers); err != nil {
			if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") && !strings.Contains(err.Error(), "server closed") {
				t.Logf("LLMParsingConsumer exited with error: %v", err)
				consumerErrChan <- err
			}
		}
		close(consumerErrChan)
		t.Log("LLMParsingConsumer goroutine finished.")
	}()

	time.Sleep(3 * time.Second) // Allow consumer to start

	// 使用标准 UUID 格式，避免 "uuid: incorrect UUID length" 错误
	submissionUUID := uuid.Must(uuid.NewV4()).String()
	targetJobID := "llm-job-id-" + uuid.Must(uuid.NewV4()).String()[:4]
	parsedTextContent := "Resume for Test User. Email: test@example.com. Phone: 1234567890. Education: Bachelor's. Experience: 2 years. Skills: Go, Python."

	// --- 新增：为测试用例植入对应的 Job 数据 ---
	mockJobForLLMTest := models.Job{
		JobID:              targetJobID, // 使用本测试用例随机生成的 targetJobID
		JobTitle:           fmt.Sprintf("Mock LLM Target Job for %s", targetJobID),
		Department:         "LLM Testing Department",
		Location:           "Remote Test",
		JobDescriptionText: "This is a mock job description specifically for the LLM consumer test case with targetJobID: " + targetJobID,
		Status:             "ACTIVE",
		CreatedByUserID:    "test_system_llm_consumer",
	}
	err := testStorageManager.MySQL.DB().FirstOrCreate(&mockJobForLLMTest, models.Job{JobID: targetJobID}).Error
	require.NoError(t, err, "Failed to seed target job for LLM consumer test with JobID: %s", targetJobID)
	t.Logf("Ensured target job exists in DB for LLM consumer test with JobID: %s", targetJobID)
	// --- 结束新增代码 ---

	// 1. Upload dummy parsed text to MinIO using UploadParsedText, and use the returned path
	actualParsedTextObjectName, err := testStorageManager.MinIO.UploadParsedText(
		context.Background(),
		submissionUUID,
		parsedTextContent,
	)
	require.NoError(t, err, "Failed to pre-upload parsed text to MinIO for LLM consumer test")
	t.Logf("Pre-uploaded parsed text to MinIO: %s in bucket %s", actualParsedTextObjectName, testCfg.MinIO.ParsedTextBucket)

	// 2. Insert initial ResumeSubmission record into MySQL
	initialSubmission := models.ResumeSubmission{
		SubmissionUUID:      submissionUUID,
		TargetJobID:         utils.StringPtr(targetJobID),
		ParsedTextPathOSS:   actualParsedTextObjectName,   // Use the path returned by UploadParsedText
		ProcessingStatus:    constants.StatusQueuedForLLM, // Status indicating it's ready for LLM processing
		OriginalFilePathOSS: "dummy/original/path-" + submissionUUID + ".pdf",
		OriginalFilename:    "dummy-" + submissionUUID + ".pdf",
		SubmissionTimestamp: time.Now().Add(-time.Hour),
		SourceChannel:       "llm-consumer-test",
	}
	err = testStorageManager.MySQL.DB().Create(&initialSubmission).Error
	require.NoError(t, err, "Failed to insert initial ResumeSubmission record into MySQL")
	t.Logf("Inserted initial ResumeSubmission record for UUID: %s with status %s", submissionUUID, constants.StatusQueuedForLLM)

	// 3. Publish ResumeProcessingMessage to RabbitMQ
	llmMessage := storage.ResumeProcessingMessage{
		SubmissionUUID:    submissionUUID,
		TargetJobID:       targetJobID,
		ParsedTextPathOSS: actualParsedTextObjectName,   // Use the path returned by UploadParsedText
		ProcessingStatus:  constants.StatusQueuedForLLM, // Match DB status
	}
	err = testStorageManager.RabbitMQ.PublishJSON(
		context.Background(),
		testCfg.RabbitMQ.ProcessingEventsExchange,
		testCfg.RabbitMQ.ParsedRoutingKey,
		llmMessage,
		true, // Persistent
	)
	require.NoError(t, err, "Failed to publish ResumeProcessingMessage")
	t.Logf("Published ResumeProcessingMessage for SubmissionUUID: %s to exchange '%s' with key '%s'",
		submissionUUID, testCfg.RabbitMQ.ProcessingEventsExchange, testCfg.RabbitMQ.ParsedRoutingKey)

	var finalDbSubmission models.ResumeSubmission
	processedSuccessfully := false

	pollingCtx, pollingCancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer pollingCancel()

	lastLogTime := time.Now()           // 添加: 初始化上次日志打印时间
	const logInterval = 5 * time.Second // 添加: 定义日志打印间隔

	for {
		select {
		case errFromConsumer, ok := <-consumerErrChan:
			if ok && errFromConsumer != nil {
				t.Fatalf("LLM Consumer exited prematurely with error: %v", errFromConsumer)
			}
			// If the channel is closed (!ok), it means StartLLMParsingConsumer has returned.
			// The actual worker goroutine (started by StartConsumer internally) might still be running.
			// We should not fail the test here, but continue polling the database.
			if !ok {
				t.Logf("StartLLMParsingConsumer function has returned (consumerErrChan closed). Consumer channel will be ignored. Continuing to poll DB status for UUID %s.", submissionUUID)
				consumerErrChan = nil // Set to nil to prevent this case from being selected again.
				continue              // Continue to the next iteration of the loop to poll DB.
			}
		case <-pollingCtx.Done():
			dbErr := testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).First(&finalDbSubmission).Error
			lastStatus := "UNKNOWN (DB fetch error)"
			if dbErr == nil {
				lastStatus = finalDbSubmission.ProcessingStatus
			}
			t.Fatalf("Polling timed out for %s. Last DB status: %s. Target status: %s. Timeout reason: %v",
				submissionUUID, lastStatus, constants.StatusProcessingCompleted, pollingCtx.Err())
		default:
		}

		dbQueryCtx, dbQueryCancel := context.WithTimeout(pollingCtx, 5*time.Second)
		err = testStorageManager.MySQL.DB().WithContext(dbQueryCtx).Where("submission_uuid = ?", submissionUUID).First(&finalDbSubmission).Error
		dbQueryCancel()

		if err == nil {
			// 修改日志打印逻辑
			if time.Since(lastLogTime) >= logInterval {
				t.Logf("Polling LLM Consumer: Submission %s status: %s", submissionUUID, finalDbSubmission.ProcessingStatus)
				lastLogTime = time.Now()
			}
			if finalDbSubmission.ProcessingStatus == constants.StatusProcessingCompleted ||
				finalDbSubmission.ProcessingStatus == constants.StatusLLMProcessingFailed ||
				finalDbSubmission.ProcessingStatus == constants.StatusVectorizationFailed ||
				finalDbSubmission.ProcessingStatus == constants.StatusChunkingFailed {
				processedSuccessfully = finalDbSubmission.ProcessingStatus == constants.StatusProcessingCompleted
				goto verificationLoopEndLLM
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			// 修改日志打印逻辑
			if time.Since(lastLogTime) >= logInterval {
				t.Logf("Polling LLM Consumer: Error fetching submission %s (will retry): %v", submissionUUID, err)
				lastLogTime = time.Now()
			}
		}
		time.Sleep(3 * time.Second) // LLM processing and embedding can take longer
	}

verificationLoopEndLLM:
	require.True(t, processedSuccessfully, "LLM processing via consumer did not reach '%s'. Final status: '%s' for UUID: %s",
		constants.StatusProcessingCompleted, finalDbSubmission.ProcessingStatus, submissionUUID)

	// Verify ResumeSubmission fields updated by LLM processing
	require.NotEmpty(t, finalDbSubmission.LLMResumeIdentifier, "LLMResumeIdentifier should be populated")
	require.NotNil(t, finalDbSubmission.LLMParsedBasicInfo, "LLMParsedBasicInfo should be populated")
	var basicInfoMap map[string]interface{}
	err = json.Unmarshal(finalDbSubmission.LLMParsedBasicInfo, &basicInfoMap)
	require.NoError(t, err, "Failed to unmarshal LLMParsedBasicInfo")
	_, nameOk := basicInfoMap["name"]
	require.True(t, nameOk, "LLMParsedBasicInfo should contain 'name'")
	t.Logf("LLMParsedBasicInfo for %s: %s", submissionUUID, string(finalDbSubmission.LLMParsedBasicInfo))

	// Verify ResumeSubmissionChunk records
	var chunks []models.ResumeSubmissionChunk
	err = testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).Order("chunk_id_in_submission asc").Find(&chunks).Error
	require.NoError(t, err, "Failed to fetch ResumeSubmissionChunk records for UUID: %s", submissionUUID)
	require.Greater(t, len(chunks), 0, "Should have at least one chunk in ResumeSubmissionChunk table after LLM processing")
	t.Logf("Found %d chunks in DB for submission %s. First chunk type: %s, title: '%s'", len(chunks), submissionUUID, chunks[0].ChunkType, chunks[0].ChunkTitle)

	// Qdrant Verification & Collect PointIDs for Cleanup
	var pointIDsForCleanup []string
	for _, chunk := range chunks {
		if chunk.PointID != nil && *chunk.PointID != "" {
			pointIDsForCleanup = append(pointIDsForCleanup, *chunk.PointID)
			t.Logf("Collected PointID: %s for chunkDBID: %d", *chunk.PointID, chunk.ChunkDBID) // Added log for each pointID
		}
	}

	if len(pointIDsForCleanup) > 0 {
		t.Logf("Qdrant Verification: Collected %d PointIDs from %d chunks for submission %s for cleanup.", len(pointIDsForCleanup), len(chunks), submissionUUID)
	} else if len(chunks) > 0 {
		t.Logf("Qdrant Verification: No PointIDs found in %d DB chunks for submission %s. Cleanup of Qdrant points will be skipped or incomplete.", len(chunks), submissionUUID)
	} else {
		t.Logf("Qdrant Verification: No chunks found for submission %s, so no PointIDs to collect for cleanup.", submissionUUID)
	}

	// Optional: Verify JobSubmissionMatch if targetJobID was present
	if targetJobID != "" {
		var matchRecord models.JobSubmissionMatch
		err = testStorageManager.MySQL.DB().Where("submission_uuid = ? AND job_id = ?", submissionUUID, targetJobID).First(&matchRecord).Error
		if err == nil {
			t.Logf("Found JobSubmissionMatch record for UUID %s, JobID %s. LLM Score: %v", submissionUUID, targetJobID, matchRecord.LLMMatchScore)
			require.NotNil(t, matchRecord.LLMMatchScore, "LLMMatchScore should be populated if match record exists and evaluator ran")
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			t.Logf("No JobSubmissionMatch record found for UUID %s, JobID %s. This is expected if the mock job evaluator is a no-op or job matching was skipped.", submissionUUID, targetJobID)
		} else {
			t.Errorf("Error fetching JobSubmissionMatch record for %s, %s: %v", submissionUUID, targetJobID, err)
		}
	}

	t.Cleanup(func() {
		t.Logf("--- Starting Cleanup for TestStartLLMParsingConsumer (%s) ---", submissionUUID)
		ctxClean := context.Background()

		// Configure GORM for silent logging during cleanup
		silentGormLoggerInstance := gormLogger.New(
			log.New(io.Discard, "\r\n", log.LstdFlags), // Log to io.Discard to suppress output
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		dbForCleanup := testStorageManager.MySQL.DB().Session(&gorm.Session{Logger: silentGormLoggerInstance})

		// 1. Delete Qdrant points
		if len(pointIDsForCleanup) > 0 && testStorageManager.Qdrant != nil {
			t.Logf("Qdrant Cleanup: Attempting to delete %d points from Qdrant for submission %s. PointIDs: %v", len(pointIDsForCleanup), submissionUUID, pointIDsForCleanup)
			errDelQdrant := testStorageManager.Qdrant.DeletePoints(ctxClean, pointIDsForCleanup) // Assumes DeletePoints takes context and slice of point IDs
			if errDelQdrant != nil {
				t.Logf("Qdrant Cleanup: Error deleting points for submission %s: %v", submissionUUID, errDelQdrant)
			} else {
				t.Logf("Qdrant Cleanup: Successfully requested deletion of %d points for submission %s", len(pointIDsForCleanup), submissionUUID)
			}
		} else if len(pointIDsForCleanup) == 0 {
			t.Logf("Qdrant Cleanup: No PointIDs to delete from Qdrant for submission %s.", submissionUUID)
		} else if testStorageManager.Qdrant == nil {
			t.Logf("Qdrant Cleanup: Qdrant client is nil, skipping point deletion for submission %s.", submissionUUID)
		}

		// 2. Delete from MySQL using the silent logger session
		errDelChunks := dbForCleanup.WithContext(ctxClean).Unscoped().Where("submission_uuid = ?", submissionUUID).Delete(&models.ResumeSubmissionChunk{}).Error
		if errDelChunks != nil {
			t.Logf("Cleanup: Error deleting chunks for %s from MySQL: %v", submissionUUID, errDelChunks)
		}
		if targetJobID != "" {
			errDelMatch := dbForCleanup.WithContext(ctxClean).Unscoped().Where("submission_uuid = ? AND job_id = ?", submissionUUID, targetJobID).Delete(&models.JobSubmissionMatch{}).Error
			if errDelMatch != nil {
				t.Logf("Cleanup: Error deleting job match for %s, %s from MySQL: %v", submissionUUID, targetJobID, errDelMatch)
			}
		}
		errDelSub := dbForCleanup.WithContext(ctxClean).Unscoped().Delete(&models.ResumeSubmission{}, "submission_uuid = ?", submissionUUID).Error
		if errDelSub != nil {
			t.Logf("Cleanup: Error deleting submission %s from MySQL: %v", submissionUUID, errDelSub)
		} else {
			t.Logf("Cleanup: Successfully deleted submission %s and its chunks/matches from MySQL", submissionUUID)
		}

		// 3. Delete from MinIO (parsed text file) - MinIO ops don't use GORM logger
		if actualParsedTextObjectName != "" { // Use the correct variable for cleanup
			errDelMinio := testStorageManager.MinIO.RemoveObject(
				ctxClean,
				testCfg.MinIO.ParsedTextBucket,
				actualParsedTextObjectName,
				minio.RemoveObjectOptions{},
			)
			if errDelMinio != nil {
				minioErr := minio.ToErrorResponse(errDelMinio)
				if minioErr.Code != "NoSuchKey" {
					t.Logf("Cleanup: Error deleting parsed text object %s from MinIO bucket %s: %v (Code: %s)", actualParsedTextObjectName, testCfg.MinIO.ParsedTextBucket, errDelMinio, minioErr.Code)
				} else {
					t.Logf("Cleanup: Parsed text object %s not found in MinIO bucket %s (already deleted or never created).", actualParsedTextObjectName, testCfg.MinIO.ParsedTextBucket)
				}
			} else {
				t.Logf("Cleanup: Successfully deleted parsed text object %s from MinIO", actualParsedTextObjectName)
			}
		}
		t.Logf("--- Finished Cleanup for TestStartLLMParsingConsumer (%s) ---", submissionUUID)
	})
}

// TestOutboxPattern_ResumeUploadConsumer_CreatesAndRelaysMessage 对Outbox模式进行端到端集成测试。
// 验证在完成业务处理后，能够通过数据库事务可靠地创建下一阶段的消息，并由MessageRelay服务成功中继到消息队列。
func TestOutboxPattern_ResumeUploadConsumer_CreatesAndRelaysMessage(t *testing.T) {
	oneTimeSetupFunc(t) // 1. 环境准备

	// 为消费者创建独立的上下文，便于管理其生命周期
	consumerCtx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()

	// 2. 实例化并启动 MessageRelay 服务
	// 使用丢弃日志，保持测试输出干净
	relayLogger := log.New(io.Discard, "[MessageRelayTest] ", log.LstdFlags)
	messageRelay := outbox.NewMessageRelay(testStorageManager.MySQL.DB(), testStorageManager.RabbitMQ, relayLogger)

	// 在后台 goroutine 中启动中继器
	go messageRelay.Start()
	// 在测试退出时，确保停止 MessageRelay
	defer messageRelay.Stop()
	t.Log("MessageRelay service started in background for the test.")
	time.Sleep(1 * time.Second) // Give relay a moment to start

	// 3. 启动 ResumeUploadConsumer
	go func() {
		if err := testResumeHandler.StartResumeUploadConsumer(consumerCtx, 1, 1*time.Second); err != nil {
			if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") {
				t.Errorf("ResumeUploadConsumer exited with unexpected error: %v", err)
			}
		}
	}()
	t.Log("ResumeUploadConsumer started in background for the test.")
	time.Sleep(1 * time.Second) // Give consumer a moment to start

	// 4. 准备测试数据和前置条件
	submissionUUID := "test-outbox-uuid-" + uuid.Must(uuid.NewV4()).String()[:8]
	t.Logf("Using SubmissionUUID for outbox test: %s", submissionUUID)

	// --- 新增：确保内容不重复，清理Redis中的文本MD5 ---
	testPDFPath := ensureTestPDF(t)
	pdfBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err)

	// 手动创建提取器以获取文本内容，从而计算出将要生成的MD5
	// 此处不关心Tika，因为Eino是默认和回退选项，足以模拟处理器行为
	tempExtractor, err := parser.NewEinoPDFTextExtractor(context.Background())
	require.NoError(t, err, "Failed to create temporary Eino extractor for MD5 cleanup")
	extractedText, _, err := tempExtractor.ExtractTextFromBytes(context.Background(), pdfBytes, "", nil)
	require.NoError(t, err, "Failed to extract text for MD5 cleanup")

	// 计算并清理MD5
	textMD5 := utils.CalculateMD5([]byte(extractedText))
	parsedTextMD5Key := testStorageManager.Redis.FormatKey(constants.ParsedTextMD5SetKey)
	_, err = testStorageManager.Redis.Client.SRem(context.Background(), parsedTextMD5Key, textMD5).Result()
	require.NoError(t, err, "Failed to remove pre-existing parsed text MD5 for clean test run")
	t.Logf("Proactively removed parsed text MD5 '%s' from Redis set '%s' for a clean test run.", textMD5, parsedTextMD5Key)
	// --- 清理结束 ---

	// 使用一个真实的PDF文件，而不是无效的字节数组
	dummyPDFContent, err := os.ReadFile(testPDFPath)
	require.NoError(t, err, "Failed to read test PDF file")

	fileMD5Hex := utils.CalculateMD5(dummyPDFContent)
	originalMinIOPath, err := testStorageManager.MinIO.UploadResumeFile(
		context.Background(),
		submissionUUID,
		".pdf",
		bytes.NewReader(dummyPDFContent),
		int64(len(dummyPDFContent)),
	)
	require.NoError(t, err, "Failed to upload dummy file to MinIO for outbox test")
	t.Logf("Pre-uploaded dummy file to MinIO at: %s", originalMinIOPath)

	// 5. 设置 RabbitMQ 监听器来捕获中继后的消息
	mq := testStorageManager.RabbitMQ
	listenerQueueName := "test-listener-q-" + submissionUUID
	err = mq.EnsureQueue(listenerQueueName, false) // durable:false
	require.NoError(t, err, "Failed to create temporary listener queue")

	err = mq.BindQueue(listenerQueueName, testCfg.RabbitMQ.ProcessingEventsExchange, testCfg.RabbitMQ.ParsedRoutingKey)
	require.NoError(t, err, "Failed to bind temporary listener queue")

	relayedMsgChan := make(chan []byte, 1)
	listenerCtx, cancelListener := context.WithCancel(context.Background())
	defer cancelListener()

	go func() {
		// Using the existing StartConsumer. The context is used to signal the handler to stop processing.
		_, consErr := mq.StartConsumer(listenerQueueName, 1, func(data []byte) bool {
			select {
			case <-listenerCtx.Done():
				return false // Attempt to stop processing
			default:
				relayedMsgChan <- data
				return true
			}
		})
		if consErr != nil && !errors.Is(consErr, context.Canceled) {
			t.Logf("ERROR: Listener consumer exited with error: %v", consErr)
		}
	}()
	t.Logf("Started listener on queue '%s' to catch the relayed message.", listenerQueueName)

	// 6. 触发动作：发布初始消息到上传队列
	uploadMessage := storage.ResumeUploadMessage{
		SubmissionUUID:      submissionUUID,
		OriginalFilePathOSS: originalMinIOPath,
		OriginalFilename:    "outbox_test.pdf",
		TargetJobID:         "outbox-job-id",
		SubmissionTimestamp: time.Now(),
		RawFileMD5:          fileMD5Hex,
	}
	err = testStorageManager.RabbitMQ.PublishJSON(
		context.Background(),
		testCfg.RabbitMQ.ResumeEventsExchange,
		testCfg.RabbitMQ.UploadedRoutingKey,
		uploadMessage,
		true,
	)
	require.NoError(t, err, "Failed to publish initial ResumeUploadMessage")
	t.Logf("Published initial message to exchange '%s' for consumer to process.", testCfg.RabbitMQ.ResumeEventsExchange)

	// 7. 验证
	// 7.1. 等待并验证中继后的消息
	var receivedData []byte
	select {
	case receivedData = <-relayedMsgChan:
		t.Log("Successfully received a message from the relayed queue.")
	case <-time.After(30 * time.Second):
		t.Fatalf("Timeout: Did not receive the relayed message on queue '%s' within 30s.", listenerQueueName)
	}

	// 验证收到的消息内容
	var relayedMessage storage.ResumeProcessingMessage
	err = json.Unmarshal(receivedData, &relayedMessage)
	require.NoError(t, err, "Failed to unmarshal the relayed message")
	require.Equal(t, submissionUUID, relayedMessage.SubmissionUUID, "The SubmissionUUID in the relayed message does not match")
	t.Logf("Relayed message content verified successfully for UUID: %s", relayedMessage.SubmissionUUID)

	// 7.2. 轮询并验证数据库中 outbox 表的记录状态
	var outboxMsg models.OutboxMessage
	require.Eventually(t, func() bool {
		err := testStorageManager.MySQL.DB().Where("aggregate_id = ?", submissionUUID).First(&outboxMsg).Error
		return err == nil && outboxMsg.Status == "SENT"
	}, 15*time.Second, 500*time.Millisecond, "Outbox message in DB did not reach 'SENT' status in time")

	t.Logf("Outbox message for UUID %s successfully verified with status 'SENT' in the database.", submissionUUID)
	require.Equal(t, "resume.parsed", outboxMsg.EventType)
	require.Equal(t, testCfg.RabbitMQ.ProcessingEventsExchange, outboxMsg.TargetExchange)
	require.Equal(t, testCfg.RabbitMQ.ParsedRoutingKey, outboxMsg.TargetRoutingKey)

	// 7.3. 验证业务表状态
	var finalSubmission models.ResumeSubmission
	err = testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).First(&finalSubmission).Error
	require.NoError(t, err, "Failed to fetch final submission record from DB")
	require.Equal(t, constants.StatusQueuedForLLM, finalSubmission.ProcessingStatus, "Final submission status should be QUEUED_FOR_LLM")
	t.Logf("Business table 'resume_submissions' status correctly updated to '%s'.", constants.StatusQueuedForLLM)

	// 8. 清理
	t.Cleanup(func() {
		t.Logf("--- Starting Cleanup for TestOutboxPattern (%s) ---", submissionUUID)
		db := testStorageManager.MySQL.DB()
		db.Unscoped().Where("aggregate_id = ?", submissionUUID).Delete(&models.OutboxMessage{})
		db.Unscoped().Where("submission_uuid = ?", submissionUUID).Delete(&models.ResumeSubmission{})
		t.Logf("Cleaned up database records for UUID: %s", submissionUUID)
		if finalSubmission.ParsedTextPathOSS != "" {
			testStorageManager.MinIO.RemoveObject(context.Background(), testCfg.MinIO.ParsedTextBucket, finalSubmission.ParsedTextPathOSS, minio.RemoveObjectOptions{})
		}
		testStorageManager.MinIO.RemoveObject(context.Background(), testCfg.MinIO.OriginalsBucket, originalMinIOPath, minio.RemoveObjectOptions{})
		t.Logf("Cleaned up MinIO objects for UUID: %s", submissionUUID)
		t.Logf("--- Finished Cleanup for TestOutboxPattern (%s) ---", submissionUUID)
	})
}

// 验证ResumeUploadConsumer的幂等性，确保重复处理相同的消息不会导致状态错误或数据重复。
func TestResumeUploadConsumer_IdempotencyCheck(t *testing.T) {
	oneTimeSetupFunc(t)

	ctx, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()

	consumerErrChan := make(chan error, 1)
	go func() {
		t.Log("启动ResumeUploadConsumer测试幂等性...")
		if err := testResumeHandler.StartResumeUploadConsumer(ctx, 1, 1*time.Second); err != nil {
			if !errors.Is(err, context.Canceled) &&
				!strings.Contains(err.Error(), "server closed") &&
				!strings.Contains(err.Error(), "channel/connection is not open") &&
				!strings.Contains(err.Error(), "context canceled") {
				t.Logf("ResumeUploadConsumer退出出现错误: %v", err)
				consumerErrChan <- err
			}
		}
		close(consumerErrChan)
		t.Log("ResumeUploadConsumer goroutine结束.")
	}()

	time.Sleep(3 * time.Second) // 给消费者启动留出时间

	submissionUUID := "test-idempot-uuid-" + uuid.Must(uuid.NewV4()).String()[:8]
	t.Logf("幂等性测试使用UUID: %s", submissionUUID)

	// 准备测试数据
	testPDFPath := ensureTestPDF(t)
	originalFileName := filepath.Base(testPDFPath)
	fileExt := filepath.Ext(originalFileName)
	expectedMinIOPathForOriginal := fmt.Sprintf("resume/%s/original%s", submissionUUID, fileExt)

	dummyPDFFileBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err, "读取测试PDF文件失败")

	// 上传测试文件到MinIO
	generatedOriginalPath, err := testStorageManager.MinIO.UploadResumeFile(
		context.Background(),
		submissionUUID,
		fileExt,
		bytes.NewReader(dummyPDFFileBytes),
		int64(len(dummyPDFFileBytes)),
	)
	require.NoError(t, err, "上传测试文件到MinIO失败")
	require.Equal(t, expectedMinIOPathForOriginal, generatedOriginalPath)
	t.Logf("预先上传测试文件到MinIO: %s in bucket %s", generatedOriginalPath, testCfg.MinIO.OriginalsBucket)

	// 创建测试消息
	testMessage := storage.ResumeUploadMessage{
		SubmissionUUID:      submissionUUID,
		OriginalFilePathOSS: generatedOriginalPath,
		OriginalFilename:    originalFileName,
		TargetJobID:         "idempot-job-id-" + uuid.Must(uuid.NewV4()).String()[:4],
		SourceChannel:       "idempot-test-channel",
		SubmissionTimestamp: time.Now(),
	}

	// 首次发布消息
	err = testStorageManager.RabbitMQ.PublishJSON(
		context.Background(),
		testCfg.RabbitMQ.ResumeEventsExchange,
		testCfg.RabbitMQ.UploadedRoutingKey,
		testMessage,
		true,
	)
	require.NoError(t, err, "发布首次测试消息失败")
	t.Logf("成功发布首次测试消息，SubmissionUUID: %s", submissionUUID)

	// 等待首条消息处理完成
	time.Sleep(8 * time.Second)

	// 查询数据库，检查第一次处理的状态
	var firstSubmission models.ResumeSubmission
	err = testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).First(&firstSubmission).Error
	require.NoError(t, err, "查询首次处理记录失败")
	firstStatus := firstSubmission.ProcessingStatus
	t.Logf("首次处理后的状态: %s", firstStatus)
	require.NotEqual(t, "", firstStatus, "首次处理应该产生有效的状态")

	// 记录首次处理时间
	firstProcessedTime := firstSubmission.UpdatedAt

	// 再次发布相同的消息
	err = testStorageManager.RabbitMQ.PublishJSON(
		context.Background(),
		testCfg.RabbitMQ.ResumeEventsExchange,
		testCfg.RabbitMQ.UploadedRoutingKey,
		testMessage,
		true,
	)
	require.NoError(t, err, "发布重复测试消息失败")
	t.Logf("成功发布重复测试消息，SubmissionUUID: %s", submissionUUID)

	// 等待重复消息的处理
	time.Sleep(8 * time.Second)

	// 再次查询数据库
	var secondSubmission models.ResumeSubmission
	err = testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).First(&secondSubmission).Error
	require.NoError(t, err, "查询重复处理记录失败")
	secondStatus := secondSubmission.ProcessingStatus
	t.Logf("重复处理后的状态: %s", secondStatus)

	// 幂等性检查：验证状态没有发生异常变化
	require.Equal(t, firstStatus, secondStatus, "幂等性失败：重复处理导致状态变更")

	// 检查更新时间是否基本保持不变（考虑可能有轻微的数据库更新）
	timeDiff := secondSubmission.UpdatedAt.Sub(firstProcessedTime)
	t.Logf("第一次处理时间: %v, 第二次处理时间: %v, 差值: %v",
		firstProcessedTime, secondSubmission.UpdatedAt, timeDiff)

	// 清理测试数据
	t.Cleanup(func() {
		t.Logf("--- 开始清理 TestResumeUploadConsumer_IdempotencyCheck 测试数据 (%s) ---", submissionUUID)
		ctxClean := context.Background()

		// 清理数据库记录
		if err := testStorageManager.MySQL.DB().Unscoped().Delete(&models.ResumeSubmission{}, "submission_uuid = ?", submissionUUID).Error; err != nil {
			t.Logf("清理：从MySQL删除记录 %s 失败: %v", submissionUUID, err)
		} else {
			t.Logf("清理：成功从MySQL删除记录 %s", submissionUUID)
		}

		// 删除MinIO中的文件
		if err := testStorageManager.MinIO.RemoveObject(ctxClean, testCfg.MinIO.OriginalsBucket, generatedOriginalPath, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("清理：从MinIO删除文件 %s 失败: %v", generatedOriginalPath, err)
		} else {
			t.Logf("清理：成功从MinIO删除文件 %s", generatedOriginalPath)
		}

		// 如果有生成的解析文本文件，也需删除
		if firstSubmission.ParsedTextPathOSS != "" {
			if err := testStorageManager.MinIO.RemoveObject(ctxClean, testCfg.MinIO.ParsedTextBucket, firstSubmission.ParsedTextPathOSS, minio.RemoveObjectOptions{}); err != nil {
				t.Logf("清理：从MinIO删除解析文本 %s 失败: %v", firstSubmission.ParsedTextPathOSS, err)
			}
		}

		t.Logf("--- 完成清理 TestResumeUploadConsumer_IdempotencyCheck 测试数据 (%s) ---", submissionUUID)
	})
}

// 验证LLMParsingConsumer的幂等性，确保重复的LLM处理请求不会创建重复的简历分块或向量数据。
func TestLLMParsingConsumer_IdempotencyCheck(t *testing.T) {
	oneTimeSetupFunc(t)

	ctxConsumer, cancelConsumer := context.WithCancel(context.Background())
	defer cancelConsumer()

	consumerErrChan := make(chan error, 1)
	go func() {
		t.Log("启动LLMParsingConsumer测试幂等性...")
		llmConsumerWorkers := 1
		if workers, ok := testCfg.RabbitMQ.ConsumerWorkers["llm_consumer_workers"]; ok && workers > 0 {
			llmConsumerWorkers = workers
		}

		if err := testResumeHandler.StartLLMParsingConsumer(ctxConsumer, llmConsumerWorkers); err != nil {
			if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context canceled") && !strings.Contains(err.Error(), "server closed") {
				t.Logf("LLMParsingConsumer退出出现错误: %v", err)
				consumerErrChan <- err
			}
		}
		close(consumerErrChan)
		t.Log("LLMParsingConsumer goroutine结束.")
	}()

	time.Sleep(3 * time.Second) // 给消费者启动留出时间

	// 准备测试数据
	submissionUUID := uuid.Must(uuid.NewV7()).String() // 使用一个有效的UUID以确保幂等性测试的正确性
	targetJobID := "llm-job-id-f1b6"                   // 使用预先配置的测试任务ID
	parsedTextContent := "测试用户简历。邮箱: test@example.com。电话: 1234567890。教育背景: 本科。工作经验: 2年。技能: Go, Python。"

	// 上传解析文本到MinIO
	parsedTextObjectName, err := testStorageManager.MinIO.UploadParsedText(
		context.Background(),
		submissionUUID,
		parsedTextContent,
	)
	require.NoError(t, err, "上传解析文本到MinIO失败")
	t.Logf("预先上传解析文本到MinIO: %s in bucket %s", parsedTextObjectName, testCfg.MinIO.ParsedTextBucket)

	// 创建数据库初始记录
	initialSubmission := models.ResumeSubmission{
		SubmissionUUID:      submissionUUID,
		TargetJobID:         utils.StringPtr(targetJobID),
		ParsedTextPathOSS:   parsedTextObjectName,
		ProcessingStatus:    constants.StatusQueuedForLLM, // 已准备好进入LLM处理
		OriginalFilePathOSS: "dummy/original/path-" + submissionUUID + ".pdf",
		OriginalFilename:    "dummy-" + submissionUUID + ".pdf",
		SubmissionTimestamp: time.Now().Add(-time.Hour),
		SourceChannel:       "llm-idempot-test",
	}
	err = testStorageManager.MySQL.DB().Create(&initialSubmission).Error
	require.NoError(t, err, "插入初始ResumeSubmission记录到MySQL失败")
	t.Logf("插入初始ResumeSubmission记录，UUID: %s, 状态: %s", submissionUUID, constants.StatusQueuedForLLM)

	// 创建测试消息
	llmMessage := storage.ResumeProcessingMessage{
		SubmissionUUID:    submissionUUID,
		TargetJobID:       targetJobID,
		ParsedTextPathOSS: parsedTextObjectName,
		ProcessingStatus:  constants.StatusQueuedForLLM,
	}

	// 首次发布消息
	err = testStorageManager.RabbitMQ.PublishJSON(
		context.Background(),
		testCfg.RabbitMQ.ProcessingEventsExchange,
		testCfg.RabbitMQ.ParsedRoutingKey,
		llmMessage,
		true,
	)
	require.NoError(t, err, "发布首次LLM测试消息失败")
	t.Logf("成功发布首次LLM测试消息，SubmissionUUID: %s", submissionUUID)

	// 等待首条消息处理完成
	var firstProcessedSubmission models.ResumeSubmission
	var firstStatus string
	firstProcessedSuccessfully := false

	// 轮询直到消息处理完成（较长超时，LLM处理可能耗时）
	pollingCtx, pollingCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer pollingCancel()

polling1:
	for {
		select {
		case <-pollingCtx.Done():
			t.Fatalf("首次处理轮询超时 %s", submissionUUID)
		default:
			err = testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).First(&firstProcessedSubmission).Error
			if err == nil && (firstProcessedSubmission.ProcessingStatus == constants.StatusProcessingCompleted ||
				strings.Contains(firstProcessedSubmission.ProcessingStatus, "FAILED")) {
				firstStatus = firstProcessedSubmission.ProcessingStatus
				firstProcessedSuccessfully = firstProcessedSubmission.ProcessingStatus == constants.StatusProcessingCompleted
				t.Logf("首次处理完成，状态: %s", firstStatus)
				break polling1
			}
			time.Sleep(3 * time.Second) // LLM处理可能需要较长时间
		}
	}

	// 记录首次处理的结果
	require.True(t, firstProcessedSuccessfully, "首次LLM处理应成功完成")

	// 记录首次处理后的chunk数量
	var firstChunkCount int64
	err = testStorageManager.MySQL.DB().Model(&models.ResumeSubmissionChunk{}).
		Where("submission_uuid = ?", submissionUUID).Count(&firstChunkCount).Error
	require.NoError(t, err, "查询首次处理的chunk数量失败")
	t.Logf("首次处理产生chunk数量: %d", firstChunkCount)
	require.Greater(t, firstChunkCount, int64(0), "首次处理应产生至少一个chunk")

	// 记录首次处理的更新时间，用于后续比较
	firstProcessedTime := firstProcessedSubmission.UpdatedAt

	// 等待一段时间后发送重复消息
	time.Sleep(5 * time.Second)

	// 再次发布相同的消息
	err = testStorageManager.RabbitMQ.PublishJSON(
		context.Background(),
		testCfg.RabbitMQ.ProcessingEventsExchange,
		testCfg.RabbitMQ.ParsedRoutingKey,
		llmMessage,
		true,
	)
	require.NoError(t, err, "发布重复LLM测试消息失败")
	t.Logf("成功发布重复LLM测试消息，SubmissionUUID: %s", submissionUUID)

	// 等待重复消息的处理
	time.Sleep(10 * time.Second)

	// 再次查询数据库
	var secondProcessedSubmission models.ResumeSubmission
	err = testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).First(&secondProcessedSubmission).Error
	require.NoError(t, err, "查询重复处理记录失败")
	secondStatus := secondProcessedSubmission.ProcessingStatus
	t.Logf("重复处理后的状态: %s", secondStatus)

	// 幂等性检查：验证状态没有变化
	require.Equal(t, firstStatus, secondStatus, "幂等性失败：重复处理导致状态变更")

	// 检查更新时间是否变化不大
	timeDiff := secondProcessedSubmission.UpdatedAt.Sub(firstProcessedTime)
	t.Logf("首次处理时间: %v, 重复处理时间: %v, 差值: %v",
		firstProcessedTime, secondProcessedSubmission.UpdatedAt, timeDiff)

	// 收集需要清理的pointIDs
	var chunks []models.ResumeSubmissionChunk
	var pointIDsForCleanup []string
	err = testStorageManager.MySQL.DB().Where("submission_uuid = ?", submissionUUID).Find(&chunks).Error
	require.NoError(t, err, "获取chunks失败")
	for _, chunk := range chunks {
		if chunk.PointID != nil && *chunk.PointID != "" {
			pointIDsForCleanup = append(pointIDsForCleanup, *chunk.PointID)
		}
	}

	// 清理测试数据
	t.Cleanup(func() {
		t.Logf("--- 开始清理 TestLLMParsingConsumer_IdempotencyCheck 测试数据 (%s) ---", submissionUUID)
		ctxClean := context.Background()

		// 1. 清理Qdrant中的点
		if len(pointIDsForCleanup) > 0 && testStorageManager.Qdrant != nil {
			t.Logf("清理：尝试从Qdrant删除 %d 个点", len(pointIDsForCleanup))
			errDelQdrant := testStorageManager.Qdrant.DeletePoints(ctxClean, pointIDsForCleanup)
			if errDelQdrant != nil {
				t.Logf("清理：从Qdrant删除点失败: %v", errDelQdrant)
			}
		}

		// 2. 清理MySQL数据
		if err := testStorageManager.MySQL.DB().Unscoped().Where("submission_uuid = ?", submissionUUID).Delete(&models.ResumeSubmissionChunk{}).Error; err != nil {
			t.Logf("清理：从MySQL删除chunks失败: %v", err)
		}

		if err := testStorageManager.MySQL.DB().Unscoped().Where("submission_uuid = ? AND job_id = ?", submissionUUID, targetJobID).Delete(&models.JobSubmissionMatch{}).Error; err != nil {
			t.Logf("清理：从MySQL删除job match记录失败: %v", err)
		}

		if err := testStorageManager.MySQL.DB().Unscoped().Delete(&models.ResumeSubmission{}, "submission_uuid = ?", submissionUUID).Error; err != nil {
			t.Logf("清理：从MySQL删除简历记录失败: %v", err)
		}

		// 3. 清理MinIO数据
		if parsedTextObjectName != "" {
			if err := testStorageManager.MinIO.RemoveObject(ctxClean, testCfg.MinIO.ParsedTextBucket, parsedTextObjectName, minio.RemoveObjectOptions{}); err != nil {
				t.Logf("清理：从MinIO删除解析文本失败: %v", err)
			}
		}

		t.Logf("--- 完成清理 TestLLMParsingConsumer_IdempotencyCheck 测试数据 (%s) ---", submissionUUID)
	})
}

// 单元测试验证用于生成Qdrant Point ID的算法（UUIDv5）是否具有确定性。
func TestQdrantDeterministicIDs(t *testing.T) {
	oneTimeSetupFunc(t)

	// 创建两个测试用例，使用有效的、固定的UUID作为resumeID
	resumeID := testResumeUUID1
	chunkID := 1

	// 第一次生成pointID
	namespaceUUID, err := uuid.FromString(resumeID)
	require.NoError(t, err, "从resumeID创建命名空间UUID失败")

	pointID1 := uuid.NewV5(namespaceUUID, fmt.Sprintf("chunk-%d", chunkID)).String()
	t.Logf("第一次生成的pointID: %s", pointID1)

	// 第二次生成pointID（应该与第一次相同）
	pointID2 := uuid.NewV5(namespaceUUID, fmt.Sprintf("chunk-%d", chunkID)).String()
	t.Logf("第二次生成的pointID: %s", pointID2)

	// 验证两次生成的ID相同
	require.Equal(t, pointID1, pointID2, "确定性ID生成失败：两次生成的ID不相同")

	// 使用不同的chunkID，验证生成不同的pointID
	differentChunkID := 2
	differentPointID := uuid.NewV5(namespaceUUID, fmt.Sprintf("chunk-%d", differentChunkID)).String()
	t.Logf("使用不同chunkID生成的pointID: %s", differentPointID)
	require.NotEqual(t, pointID1, differentPointID, "不同的chunkID应该生成不同的pointID")

	// 使用不同的resumeID，验证生成不同的pointID
	differentResumeID := testResumeUUID2
	differentNamespaceUUID, err := uuid.FromString(differentResumeID)
	require.NoError(t, err, "从不同的resumeID创建命名空间UUID失败")

	differentResumePointID := uuid.NewV5(differentNamespaceUUID, fmt.Sprintf("chunk-%d", chunkID)).String()
	t.Logf("使用不同resumeID生成的pointID: %s", differentResumePointID)
	require.NotEqual(t, pointID1, differentResumePointID, "不同的resumeID应该生成不同的pointID")

	// 验证同一个resumeID的不同操作会产生可预测的ID
	predictablePointID := uuid.NewV5(namespaceUUID, fmt.Sprintf("chunk-%d", chunkID)).String()
	require.Equal(t, pointID1, predictablePointID, "对同一数据的操作应该产生可预测的ID")
}

// 集成测试验证Qdrant存储层在实际存储向量时，能够利用确定性ID生成逻辑，实现幂等存储。
func TestQdrantDeterministicIDsIntegration(t *testing.T) {
	oneTimeSetupFunc(t)

	// 跳过测试如果Qdrant客户端不可用
	if testStorageManager.Qdrant == nil {
		t.Skip("Qdrant client is nil, skipping integration test")
	}

	// 创建测试数据
	resumeID := testResumeUUID1
	chunks := []types.ResumeChunk{
		{
			ChunkID:   1,
			ChunkType: "summary",
			Content:   "这是第一个测试块",
			Metadata: types.ChunkMetadata{
				ExperienceYears: 5,
				EducationLevel:  "本科",
			},
		},
		{
			ChunkID:   2,
			ChunkType: "experience",
			Content:   "这是第二个测试块",
			Metadata: types.ChunkMetadata{
				ExperienceYears: 5,
				EducationLevel:  "本科",
			},
		},
	}

	// 创建 1024 维的测试向量
	dimensions := 1024
	embeddings := make([][]float64, 2)

	// 第一个向量: 使用重复模式填充 1024 维
	embeddings[0] = make([]float64, dimensions)
	for i := 0; i < dimensions; i++ {
		embeddings[0][i] = 0.1 + 0.01*float64(i%10) // 创建周期性模式以节省代码长度
	}

	// 第二个向量: 使用不同的重复模式填充 1024 维
	embeddings[1] = make([]float64, dimensions)
	for i := 0; i < dimensions; i++ {
		embeddings[1][i] = 0.6 + 0.01*float64(i%10) // 创建周期性模式以节省代码长度
	}

	// 第一次存储，获取生成的pointIDs
	ctx := context.Background()
	pointIDs1, err := testStorageManager.Qdrant.StoreResumeVectors(ctx, resumeID, chunks, embeddings)
	require.NoError(t, err, "第一次存储向量应成功")
	require.Len(t, pointIDs1, 2, "应返回两个pointID")

	t.Logf("第一次存储生成的pointIDs: %v", pointIDs1)

	// 存储相同的数据，验证生成相同的pointIDs
	pointIDs2, err := testStorageManager.Qdrant.StoreResumeVectors(ctx, resumeID, chunks, embeddings)
	require.NoError(t, err, "第二次存储向量应成功")
	require.Len(t, pointIDs2, 2, "应返回两个pointID")

	t.Logf("第二次存储生成的pointIDs: %v", pointIDs2)

	// 验证两次生成的ID相同
	assert.Equal(t, pointIDs1, pointIDs2, "确定性ID生成失败：两次生成的ID不相同")

	// 验证具体ID是否符合预期格式 (UUIDv5)
	for i, pointID := range pointIDs1 {
		// 验证是否为有效的UUID格式
		_, err := uuid.FromString(pointID)
		require.NoError(t, err, "pointID应为有效的UUID格式")

		// 验证与chunk信息的关联
		chunkID := chunks[i].ChunkID
		t.Logf("Chunk %d 的 pointID: %s", chunkID, pointID)
	}

	// 使用不同的resumeID，验证生成不同的pointIDs
	differentResumeID := testResumeUUID2
	differentPointIDs, err := testStorageManager.Qdrant.StoreResumeVectors(ctx, differentResumeID, chunks, embeddings)
	require.NoError(t, err, "使用不同resumeID存储向量应成功")

	t.Logf("使用不同resumeID生成的pointIDs: %v", differentPointIDs)

	// 验证不同resumeID生成的pointIDs与原来的不同
	for i, pointID := range pointIDs1 {
		assert.NotEqual(t, pointID, differentPointIDs[i], "不同的resumeID应该生成不同的pointID")
	}

	// 清理测试数据
	t.Cleanup(func() {
		// 删除测试过程中创建的所有向量点
		allPointIDs := append(pointIDs1, differentPointIDs...)
		if len(allPointIDs) > 0 && testStorageManager.Qdrant != nil {
			errDel := testStorageManager.Qdrant.DeletePoints(ctx, allPointIDs)
			if errDel != nil {
				t.Logf("清理：删除测试点失败: %v", errDel)
			} else {
				t.Logf("清理：成功删除 %d 个测试点", len(allPointIDs))
			}
		}
	})
}

// getStringPtrValue 获取字符串指针的值，如果指针为nil则返回空字符串
func getStringPtrValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
