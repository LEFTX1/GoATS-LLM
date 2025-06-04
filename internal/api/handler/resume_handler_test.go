package handler_test

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/api/handler"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	appCoreLogger "ai-agent-go/internal/logger"
	parser "ai-agent-go/internal/parser"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/pkg/utils"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/gofrs/uuid/v5"
	minio "github.com/minio/minio-go/v7"
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
		t.Logf("警告: 阿里云 API Key 未在配置文件 (%s) 中有效配置。", testConfigPath)
		t.Logf("依赖真实LLM的测试将使用 MockLLMModel 作为回退。")
		mockLLM := &processor.MockLLMModel{}
		llmForChunker = mockLLM
		llmForEvaluator = mockLLM
	} else {
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
	}
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

func TestHandleResumeUpload_Success_NewFile(t *testing.T) {
	oneTimeSetupFunc(t)

	testPDFPath := ensureTestPDF(t)
	targetJobID := "job_test_new_" + uuid.Must(uuid.NewV4()).String()[:8]
	sourceChannel := "test_upload_new"

	fileBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err)
	fileMD5Hex := utils.CalculateMD5(fileBytes)
	rawFileKey := testStorageManager.Redis.FormatKey(constants.RawFileMD5SetKey)
	ctx := context.Background()

	isMember, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	if isMember {
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		require.NoError(t, err, "Failed to remove pre-existing MD5 for clean test run")
		t.Logf("Removed pre-existing MD5 %s from Redis for clean test run (Success_NewFile)", fileMD5Hex)
	}

	body, contentType := createMultipartForm(t, testPDFPath, targetJobID, sourceChannel)

	resp := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body, Len: body.Len()},
		ut.Header{Key: "Content-Type", Value: contentType},
	)
	require.Equal(t, http.StatusOK, resp.Code)

	var uploadResp handler.ResumeUploadResponse
	err = json.Unmarshal(resp.Body.Bytes(), &uploadResp)
	require.NoError(t, err)
	require.Equal(t, constants.StatusSubmittedForProcessing, uploadResp.Status)
	require.NotEmpty(t, uploadResp.SubmissionUUID, "SubmissionUUID should not be empty")

	actualMinIOPathInHandler := fmt.Sprintf("resume/%s/original%s", uploadResp.SubmissionUUID, filepath.Ext(testPDF))
	_, err = testStorageManager.MinIO.StatObject(ctx, testCfg.MinIO.OriginalsBucket, actualMinIOPathInHandler, minio.StatObjectOptions{})
	require.NoError(t, err, "File not found in MinIO. Expected path: %s in bucket %s", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket)
	t.Logf("Successfully verified file exists in MinIO at %s in bucket %s", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket)

	isMemberAfter, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	require.True(t, isMemberAfter, "File MD5 hex should be in Redis set after successful upload")
	t.Logf("Successfully verified MD5 %s is in Redis set %s after test.", fileMD5Hex, rawFileKey)

	t.Cleanup(func() {
		t.Logf("--- Starting Cleanup for TestHandleResumeUpload_Success_NewFile (%s) ---", uploadResp.SubmissionUUID)
		err := testStorageManager.MinIO.RemoveObject(ctx, testCfg.MinIO.OriginalsBucket, actualMinIOPathInHandler, minio.RemoveObjectOptions{})
		if err != nil {
			t.Logf("Cleanup: Failed to remove object %s from MinIO bucket %s: %v", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket, err)
		} else {
			t.Logf("Cleanup: Successfully removed object %s from MinIO bucket %s", actualMinIOPathInHandler, testCfg.MinIO.OriginalsBucket)
		}
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		if err != nil && err.Error() != "redis: nil" && err != redis.Nil {
			t.Logf("Cleanup: Failed to remove MD5 %s from Redis set %s: %v", fileMD5Hex, rawFileKey, err)
		} else {
			t.Logf("Cleanup: Successfully removed MD5 %s from Redis set %s", fileMD5Hex, rawFileKey)
		}
		t.Logf("--- Finished Cleanup for TestHandleResumeUpload_Success_NewFile (%s) ---", uploadResp.SubmissionUUID)
	})
}

func TestHandleResumeUpload_DuplicateFile(t *testing.T) {
	oneTimeSetupFunc(t)

	testPDFPath := ensureTestPDF(t)
	targetJobIDBase := "job_test_dup_" + uuid.Must(uuid.NewV4()).String()[:8]
	sourceChannel := "test_upload_dup"

	fileBytes, err := os.ReadFile(testPDFPath)
	require.NoError(t, err)
	fileMD5Hex := utils.CalculateMD5(fileBytes)
	rawFileKey := testStorageManager.Redis.FormatKey(constants.RawFileMD5SetKey)
	ctx := context.Background()

	isMemberInitial, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	if isMemberInitial {
		_, err = testStorageManager.Redis.Client.SRem(ctx, rawFileKey, fileMD5Hex).Result()
		require.NoError(t, err, "Failed to remove pre-existing MD5 for clean duplicate test run")
		t.Logf("Removed pre-existing MD5 %s from Redis for clean duplicate test run", fileMD5Hex)
	}

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

	isMemberAfterFirst, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	require.True(t, isMemberAfterFirst, "File MD5 should be in Redis after first successful upload")
	t.Logf("MD5 %s added to Redis after first upload.", fileMD5Hex)

	t.Logf("Performing second (duplicate) upload for JobID: %s-2", targetJobIDBase)
	body2, contentType2 := createMultipartForm(t, testPDFPath, targetJobIDBase+"-2", sourceChannel)
	resp2 := ut.PerformRequest(testHertzEngine.Engine, "POST", "/api/v1/resume/upload",
		&ut.Body{Body: body2, Len: body2.Len()},
		ut.Header{Key: "Content-Type", Value: contentType2},
	)

	require.Equal(t, http.StatusOK, resp2.Code, "Duplicate upload should also return 200 OK")
	var uploadResp2 handler.ResumeUploadResponse
	err = json.Unmarshal(resp2.Body.Bytes(), &uploadResp2)
	require.NoError(t, err)
	require.Equal(t, constants.StatusDuplicateFileSkipped, uploadResp2.Status, "Second upload of the same file should be skipped")
	require.Empty(t, uploadResp2.SubmissionUUID, "SubmissionUUID should be empty for skipped duplicate")

	isMemberAfterSecond, err := testStorageManager.Redis.Client.SIsMember(ctx, rawFileKey, fileMD5Hex).Result()
	require.NoError(t, err)
	require.True(t, isMemberAfterSecond, "File MD5 should still be in Redis after duplicate upload attempt")

	_, err = testStorageManager.MinIO.StatObject(ctx, testCfg.MinIO.OriginalsBucket, firstMinIOPath, minio.StatObjectOptions{})
	require.NoError(t, err, "First uploaded file should still exist in MinIO")

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
				processedSuccessfully = (finalStatus == constants.StatusQueuedForLLM || finalStatus == constants.StatusContentDuplicateSkipped)
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

// TestStartLLMParsingConsumer_ProcessMessageSuccess tests the LLM parsing consumer.
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

	submissionUUID := "test-llm-consum-uuid-" + uuid.Must(uuid.NewV4()).String()[:8]
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

	pollingCtx, pollingCancel := context.WithTimeout(context.Background(), 90*time.Second) // Increased timeout
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
				processedSuccessfully = (finalDbSubmission.ProcessingStatus == constants.StatusProcessingCompleted)
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

	// Qdrant Verification:
	t.Logf("Qdrant Verification: For submission %s, process completed. Points are assumed to be stored in Qdrant.", submissionUUID)
	qdrantPointsExist := true // For cleanup logic, assume points were created if process reached here successfully.

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

		// 1. Delete Qdrant points (This is a placeholder, actual Qdrant client calls would go here if needed for cleanup)
		if qdrantPointsExist && len(chunks) > 0 {
			t.Logf("Qdrant Cleanup: For submission %s, %d chunks were processed. Manual Qdrant cleanup or enhanced test logic needed for point deletion without known IDs.", submissionUUID, len(chunks))
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
