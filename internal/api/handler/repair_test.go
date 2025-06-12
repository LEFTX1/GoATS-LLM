package handler

import (
	"ai-agent-go/internal/agent"
	"ai-agent-go/internal/config"
	appCoreLogger "ai-agent-go/internal/logger"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	glog "github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/common/json"
	"github.com/gofrs/uuid/v5"
	hertzadapter "github.com/hertz-contrib/logger/zerolog"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

// 全局变量
var (
	setupDone          bool
	testCfg            *config.Config
	testConfigPath     = "../../config/config.yaml" // 默认配置路径
	testStorageManager *storage.Storage
	testResumeHandler  *ResumeHandler
	testHertzEngine    *server.Hertz
)

// oneTimeSetupFunc 初始化测试环境
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

	//从env文件读取apikey
	// 构建多个可能的路径
	envPaths := []string{
		filepath.Join(testConfigPath, ".env"), // 当前目录
		".env",                                // 相对路径
		filepath.Join(testConfigPath, "../../../.env"),      // 假设从internal/api/handler运行
		filepath.Join(filepath.Dir(testConfigPath), ".env"), // 配置文件同级目录
	}

	// 遍历路径，找到第一个存在的.env文件
	for _, envPath := range envPaths {
		if _, err := os.Stat(envPath); err == nil {
			// 找到.env文件，加载它
			if err := godotenv.Load(envPath); err != nil {
				t.Logf("Warning: Failed to load .env file from %s: %v", envPath, err)
			} else {
				t.Logf("Successfully loaded .env file from %s", envPath)
			}
			break
		}
	}

	// 优先从环境变量获取API密钥
	apiKey := os.Getenv("DASHSCOPE_API_KEY")
	testCfg.Aliyun.APIKey = apiKey // 确保配置中也更新了API Key
	apiURL := testCfg.Aliyun.APIURL
	if apiURL == "" {
		apiURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions" // Default Dashscope-compatible endpoint
		t.Logf("Aliyun API URL not set in config, using default: %s", apiURL)
	}

	if apiKey == "" || apiKey == "your_api_key_here" || apiKey == "test_api_key" {
		t.Logf("未检测到有效的阿里云API Key，将跳过需要LLM的测试")
		setupDone = true
		return
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
		t.Logf("Chunker: 初始化真实LLM模型 (%s) 失败: %v。将跳过依赖LLM的测试。", chunkerModelName, llmInitErr)
		setupDone = true
		return
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
		t.Logf("Evaluator: 初始化真实LLM模型 (%s) 失败: %v。将跳过依赖LLM的测试。", evaluatorModelName, llmInitErr)
		setupDone = true
		return
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
	)

	testResumeHandler = NewResumeHandler(testCfg, testStorageManager, resumeProcessor)
	require.NotNil(t, testResumeHandler, "Test resume handler is nil")

	testHertzEngine = server.New(server.WithHostPorts("127.0.0.1:0")) // Use random available port
	rg := testHertzEngine.Group("/api/v1")
	rg.POST("/resume/upload", func(c context.Context, appCtx *app.RequestContext) {
		testResumeHandler.HandleResumeUpload(c, appCtx)
	})

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

	setupDone = true
	t.Log("oneTimeSetupFunc completed.")
}

// 返回默认的基本信息
func getDefaultBasicInfo() map[string]string {
	return map[string]string{
		"name":                "未知",
		"phone":               "未知",
		"email":               "未知",
		"years_of_experience": "0",
		"highest_education":   "未知",
	}
}

// ResumeChunkData 表示CSV中的简历块数据
type ResumeChunkData struct {
	ChunkDBID           uint64
	SubmissionUUID      string
	ChunkIDInSubmission int
	ChunkType           string
	ChunkTitle          string
	ChunkContentText    string
	PointID             string
}

// 从CSV文件加载简历块数据
func loadResumeChunksFromCSV(t *testing.T, filePath string) ([]ResumeChunkData, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}

	var chunks []ResumeChunkData
	// 跳过标题行
	for i, record := range records {
		if i == 0 { // 标题行
			continue
		}

		// 确保记录有足够的字段
		if len(record) < 7 {
			t.Logf("警告: 第 %d 行记录字段不足，跳过", i+1)
			continue
		}

		chunkDBID, err := strconv.ParseUint(record[0], 10, 64)
		if err != nil {
			t.Logf("警告: 第 %d 行无效的ChunkDBID: %s, 错误: %v", i+1, record[0], err)
			continue
		}

		chunkIDInSubmission, err := strconv.Atoi(record[2])
		if err != nil {
			t.Logf("警告: 第 %d 行无效的ChunkIDInSubmission: %s, 错误: %v", i+1, record[2], err)
			continue
		}

		chunk := ResumeChunkData{
			ChunkDBID:           chunkDBID,
			SubmissionUUID:      record[1],
			ChunkIDInSubmission: chunkIDInSubmission,
			ChunkType:           record[3],
			ChunkTitle:          record[4],
			ChunkContentText:    record[5],
			PointID:             record[6],
		}
		if chunk.ChunkTitle == "" {
			panic("ChunkTitle不能为空，检查CSV文件格式")
		}

		// 跳过BASIC_INFO类型的分块
		if chunk.ChunkType == "BASIC_INFO" {
			t.Logf("跳过BASIC_INFO类型的分块: ChunkDBID=%d, SubmissionUUID=%s",
				chunk.ChunkDBID, chunk.SubmissionUUID)
			continue
		}

		// 处理所有非BASIC_INFO类型的块，进行全量重建
		chunks = append(chunks, chunk)
	}

	t.Logf("从CSV文件加载了 %d 条有效记录(不含BASIC_INFO)", len(chunks))

	// 分析文件中的chunk类型分布
	chunkTypeStats := make(map[string]int)
	pointIDStatus := map[string]int{
		"empty":  0, // 空point_id
		"filled": 0, // 已有point_id
	}

	for _, chunk := range chunks {
		chunkTypeStats[chunk.ChunkType]++

		if chunk.PointID == "" {
			pointIDStatus["empty"]++
		} else {
			pointIDStatus["filled"]++
		}
	}

	t.Logf("CSV文件中非BASIC_INFO块的类型分布:")
	for chunkType, count := range chunkTypeStats {
		t.Logf("- %s: %d 个块", chunkType, count)
	}

	t.Logf("CSV文件中point_id状态:")
	t.Logf("- 空point_id: %d 个块", pointIDStatus["empty"])
	t.Logf("- 已有point_id: %d 个块", pointIDStatus["filled"])
	return chunks, nil
}

// 按提交UUID分组
func groupChunksBySubmissionUUID(chunks []ResumeChunkData) map[string][]ResumeChunkData {
	result := make(map[string][]ResumeChunkData)
	for _, chunk := range chunks {
		result[chunk.SubmissionUUID] = append(result[chunk.SubmissionUUID], chunk)
	}
	return result
}

// 获取简历基本信息
func getBasicInfoForSubmission(ctx context.Context, db *storage.MySQL, submissionUUID string) (map[string]string, error) {
	// 检查MySQL是否为nil
	if db == nil {
		return getDefaultBasicInfo(), fmt.Errorf("MySQL存储未初始化")
	}

	// 检查db.DB()是否为nil
	dbConn := db.DB()
	if dbConn == nil {
		return getDefaultBasicInfo(), fmt.Errorf("MySQL连接未初始化")
	}

	var llmParsedBasicInfoJSON sql.NullString
	row := dbConn.WithContext(ctx).Raw(`
		SELECT IFNULL(llm_parsed_basic_info, '{}') 
		FROM resume_submissions 
		WHERE submission_uuid = ?
	`, submissionUUID).Row()

	if err := row.Scan(&llmParsedBasicInfoJSON); err != nil {
		return getDefaultBasicInfo(), err
	}

	basicInfo := make(map[string]string)
	if llmParsedBasicInfoJSON.Valid && llmParsedBasicInfoJSON.String != "" && llmParsedBasicInfoJSON.String != "{}" {
		var jsonMap map[string]interface{}
		if err := json.Unmarshal([]byte(llmParsedBasicInfoJSON.String), &jsonMap); err != nil {
			return getDefaultBasicInfo(), err
		}

		for k, v := range jsonMap {
			switch val := v.(type) {
			case string:
				basicInfo[k] = val
			case float64:
				basicInfo[k] = strconv.FormatFloat(val, 'f', 1, 64)
			case int:
				basicInfo[k] = strconv.Itoa(val)
			case bool:
				basicInfo[k] = strconv.FormatBool(val)
			default:
				// 尝试JSON编组
				if b, err := json.Marshal(v); err == nil {
					basicInfo[k] = string(b)
				}
			}
		}
	}

	// 确保关键字段存在
	if _, ok := basicInfo["name"]; !ok {
		basicInfo["name"] = "未知"
	}
	if _, ok := basicInfo["phone"]; !ok {
		basicInfo["phone"] = "未知"
	}
	if _, ok := basicInfo["email"]; !ok {
		basicInfo["email"] = "未知"
	}
	if _, ok := basicInfo["years_of_experience"]; !ok {
		basicInfo["years_of_experience"] = "0"
	}
	if _, ok := basicInfo["highest_education"]; !ok {
		basicInfo["highest_education"] = "未知"
	}

	return basicInfo, nil
}

// prepareChunksForEmbedding 准备要嵌入的简历块
func prepareChunksForEmbedding(chunks []ResumeChunkData, basicInfo map[string]string) ([]types.ResumeChunk, []string) {
	var resumeChunks []types.ResumeChunk
	var texts []string

	for _, chunk := range chunks {
		// 准备要嵌入的文本 - 基于传入的基本信息和块内容构造增强文本
		text := generateEmbeddingText(chunk, basicInfo)
		texts = append(texts, text)

		// 创建唯一标识符
		identifiers := types.Identity{
			Name:  basicInfo["name"],
			Phone: basicInfo["phone"],
			Email: basicInfo["email"],
		}

		// 创建元数据
		metadata := make(types.ChunkMetadata)

		// 添加基本元数据
		metadata["highest_education"] = basicInfo["highest_education"]
		metadata["is_211"] = basicInfo["is_211"] == "true"
		metadata["is_985"] = basicInfo["is_985"] == "true"
		metadata["is_double_top"] = basicInfo["is_double_top"] == "true"

		// 添加经验相关元数据
		metadata["has_work_exp"] = basicInfo["has_work_exp"] == "true"
		metadata["has_intern_exp"] = basicInfo["has_intern"] == "true"

		// 添加奖项相关元数据
		metadata["has_algorithm_award"] = basicInfo["has_algorithm_award"] == "true"
		metadata["has_programming_competition_award"] = basicInfo["has_programming_competition_award"] == "true"

		// 添加经验年限
		if expStr, ok := basicInfo["years_of_experience"]; ok && expStr != "" {
			if expFloat, err := strconv.ParseFloat(expStr, 32); err == nil {
				metadata["years_of_experience"] = float32(expFloat)
			}
		}

		// 创建ResumeChunk - Qdrant存储结构
		resumeChunks = append(resumeChunks, types.ResumeChunk{
			ChunkID:           chunk.ChunkIDInSubmission,
			ChunkType:         chunk.ChunkType,
			ChunkTitle:        chunk.ChunkTitle,
			Content:           chunk.ChunkContentText,
			ImportanceScore:   getImportanceScoreByType(chunk.ChunkType),
			UniqueIdentifiers: identifiers,
			Metadata:          metadata,
		})
	}

	return resumeChunks, texts
}

// generateEmbeddingText 生成要嵌入的文本
func generateEmbeddingText(chunk ResumeChunkData, basicInfo map[string]string) string {
	// 准备嵌入文本，基于分块标题和内容构建
	embeddingText := ""
	if chunk.ChunkTitle != "" {
		embeddingText = fmt.Sprintf("%s: %s", chunk.ChunkTitle, chunk.ChunkContentText)
	} else {
		embeddingText = chunk.ChunkContentText
	}

	return embeddingText
}

// 根据分块类型确定重要性分数
func getImportanceScoreByType(chunkType string) float32 {
	switch chunkType {
	case "BASIC_INFO":
		return 1.0
	case "WORK_EXPERIENCE":
		return 0.95
	case "PROJECTS":
		return 0.9
	case "INTERNSHIPS":
		return 0.85
	case "SKILLS":
		return 0.8
	case "EDUCATION":
		return 0.75
	case "AWARDS":
		return 0.7
	case "PORTFOLIO":
		return 0.65
	default:
		return 0.5
	}
}

// 更新数据库中的point_id和qdrant_point_ids
func updatePointIDsInDatabase(ctx context.Context, db *storage.MySQL, chunks []ResumeChunkData, pointIDs []string, submissionUUID string) error {
	return db.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 更新每个chunk的point_id
		for i, chunk := range chunks {
			if i < len(pointIDs) {
				if err := tx.Exec(
					"UPDATE resume_submission_chunks SET point_id = ? WHERE chunk_db_id = ?",
					pointIDs[i], chunk.ChunkDBID,
				).Error; err != nil {
					return err
				}
			} else {
				// 使用uuid/v5包的NewV4函数创建UUID
				newUUID, err := uuid.NewV4()
				if err != nil {
					return fmt.Errorf("生成UUID失败: %w", err)
				}
				newPointID := newUUID.String()

				if err := tx.Exec(
					"UPDATE resume_submission_chunks SET point_id = ? WHERE chunk_db_id = ?",
					newPointID, chunk.ChunkDBID,
				).Error; err != nil {
					return err
				}
			}
		}

		// 2. 更新resume_submissions表的qdrant_point_ids字段
		pointIDsJSON, err := json.Marshal(pointIDs)
		if err != nil {
			return err
		}

		if err := tx.Exec(
			"UPDATE resume_submissions SET qdrant_point_ids = ? WHERE submission_uuid = ?",
			string(pointIDsJSON), submissionUUID,
		).Error; err != nil {
			return err
		}

		return nil
	})
}

// TestCleanupBasicInfoVectors 清理向量数据库中所有与BASIC_INFO分块关联的向量
func TestCleanupBasicInfoVectors(t *testing.T) {
	// 使用共享测试设置，确保环境配置正确
	oneTimeSetupFunc(t)

	// 跳过CI环境，只在本地运行
	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要的存储组件是否可用
	if testStorageManager.Qdrant == nil {
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	if testStorageManager.MySQL == nil {
		t.Skip("MySQL未配置或不可用，跳过测试")
	}

	// 步骤1：查询所有BASIC_INFO类型分块的point_id
	ctx := context.Background()
	var basicInfoPointIDs []string

	err := testStorageManager.MySQL.DB().WithContext(ctx).
		Raw(`SELECT point_id FROM resume_submission_chunks 
			WHERE chunk_type = 'BASIC_INFO' AND point_id IS NOT NULL AND point_id != ''`).
		Pluck("point_id", &basicInfoPointIDs).Error

	if err != nil {
		t.Fatalf("查询BASIC_INFO分块的point_id失败: %v", err)
	}

	t.Logf("找到 %d 个BASIC_INFO分块的point_id需要清理", len(basicInfoPointIDs))

	// 如果没有找到point_id，则提前退出
	if len(basicInfoPointIDs) == 0 {
		t.Log("没有找到需要清理的BASIC_INFO分块point_id，测试完成")
		return
	}

	// 步骤2：从向量数据库删除这些向量（分批处理）
	chunks := make([]string, 0, len(basicInfoPointIDs))
	for _, pointID := range basicInfoPointIDs {
		// 只处理有效的point_id
		if strings.TrimSpace(pointID) != "" {
			chunks = append(chunks, pointID)
		}
	}

	if len(chunks) > 0 {
		// 分批删除向量，每批次最多处理25个
		batchSize := 25
		totalBatches := (len(chunks) + batchSize - 1) / batchSize // 向上取整计算批次数

		t.Logf("将分 %d 批处理总共 %d 个向量，每批最多 %d 个",
			totalBatches, len(chunks), batchSize)

		successCount := 0
		failCount := 0

		for i := 0; i < len(chunks); i += batchSize {
			// 计算当前批次的结束索引
			end := i + batchSize
			if end > len(chunks) {
				end = len(chunks)
			}

			// 获取当前批次的point_id
			batchChunks := chunks[i:end]

			// 记录当前批次信息
			t.Logf("处理第 %d/%d 批，包含 %d 个向量 (索引: %d-%d)",
				(i/batchSize)+1, totalBatches, len(batchChunks), i, end-1)

			// 添加重试逻辑
			maxRetries := 3
			var deleteErr error

			for retry := 0; retry < maxRetries; retry++ {
				if retry > 0 {
					t.Logf("第 %d 次重试删除批次 %d...", retry, (i/batchSize)+1)
					// 在重试前等待一段时间
					time.Sleep(time.Duration(retry*2) * time.Second)
				}

				// 执行删除操作
				deleteErr = testStorageManager.Qdrant.DeletePoints(ctx, batchChunks)

				// 如果成功则跳出重试循环
				if deleteErr == nil {
					break
				}

				t.Logf("删除批次 %d 失败 (重试 %d/%d): %v",
					(i/batchSize)+1, retry+1, maxRetries, deleteErr)
			}

			// 处理最终结果
			if deleteErr != nil {
				t.Logf("警告: 批次 %d 删除失败，跳过此批次: %v",
					(i/batchSize)+1, deleteErr)
				failCount += len(batchChunks)
			} else {
				t.Logf("成功删除批次 %d 中的 %d 个向量",
					(i/batchSize)+1, len(batchChunks))
				successCount += len(batchChunks)

				// 更新MySQL中对应的point_id为NULL
				// 注意：只更新成功删除的批次
				updateQuery := `UPDATE resume_submission_chunks SET point_id = NULL 
					WHERE chunk_type = 'BASIC_INFO' AND point_id IN ?`

				updateErr := testStorageManager.MySQL.DB().WithContext(ctx).
					Exec(updateQuery, batchChunks).
					Error

				if updateErr != nil {
					t.Logf("警告: 更新批次 %d 的MySQL记录失败: %v",
						(i/batchSize)+1, updateErr)
				} else {
					t.Logf("成功将批次 %d 的 %d 个BASIC_INFO分块的point_id更新为NULL",
						(i/batchSize)+1, len(batchChunks))
				}
			}

			// 添加一个小暂停，避免连续请求过快
			time.Sleep(500 * time.Millisecond)
		}

		// 汇总结果
		t.Logf("清理完成：成功删除 %d 个向量，%d 个向量失败",
			successCount, failCount)

		// 如果有失败，但不是全部失败，则测试仍然可以通过
		if failCount > 0 && failCount < len(chunks) {
			t.Logf("部分向量删除失败，但测试将继续 (%d 成功, %d 失败)",
				successCount, failCount)
		} else if failCount == len(chunks) {
			// 如果全部失败，则测试失败
			t.Fatalf("所有向量删除都失败，请检查Qdrant服务是否正常运行")
		}
	} else {
		t.Log("没有有效的point_id需要从向量数据库删除")
	}

	t.Log("BASIC_INFO向量清理完成")
}

// TestInitializeQdrantCollection 用于重建Qdrant集合
func TestInitializeQdrantCollection(t *testing.T) {
	// 使用共享测试设置，确保环境配置正确
	oneTimeSetupFunc(t)

	// 跳过CI环境，只在本地运行
	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要的存储组件是否可用
	if testStorageManager == nil {
		t.Fatal("testStorageManager未初始化")
	}

	// 创建上下文
	ctx := context.Background()

	// 1. 尝试连接Qdrant，验证可用性
	if testStorageManager.Qdrant != nil {
		t.Log("Qdrant客户端已初始化，正在检查服务可用性...")
		// 简单测试：尝试获取点数量
		count, err := testStorageManager.Qdrant.CountPoints(ctx)
		if err != nil {
			t.Logf("警告: Qdrant可能不可用: %v", err)
		} else {
			t.Logf("Qdrant可用，当前集合中有 %d 个点", count)
		}
	} else {
		// 如果未初始化，尝试根据配置创建
		t.Log("尝试初始化Qdrant客户端...")

		// 使用默认配置
		qdrantConfig := &config.QdrantConfig{
			Endpoint:   "http://localhost:6333",
			Collection: "resumes",
			Dimension:  1024,
		}

		// 创建Qdrant客户端
		qdrant, err := storage.NewQdrant(qdrantConfig)
		if err != nil {
			t.Fatalf("无法创建Qdrant客户端: %v", err)
		}

		// 保存到测试存储管理器
		testStorageManager.Qdrant = qdrant

		t.Log("Qdrant客户端初始化成功")
	}

	// 2. 测试连接和点计数
	count, err := testStorageManager.Qdrant.CountPoints(ctx)
	if err != nil {
		t.Errorf("连接Qdrant失败: %v", err)
	} else {
		t.Logf("Qdrant连接成功，当前集合中有 %d 个点", count)
	}

	// 3. 简单测试 - 创建一个示例向量并尝试存储和删除
	// 这比直接调用DeletePoints更安全，因为它使用已知有效的API流程
	t.Log("尝试创建测试向量以验证存储和删除功能...")

	// 创建测试ID和向量
	testUUID := fmt.Sprintf("test-uuid-%d", time.Now().Unix())
	// 创建一个随机向量，长度为1024（与Qdrant配置匹配）
	testVector := make([]float64, 1024)
	for i := range testVector {
		testVector[i] = rand.Float64() // 填充随机值
	}

	// 创建一个测试chunk
	testChunk := types.ResumeChunk{
		ChunkID:         1,
		ChunkType:       "TEST",
		Content:         "This is a test chunk for Qdrant validation",
		ImportanceScore: 1.0,
		UniqueIdentifiers: types.Identity{
			Name:  "Test Name",
			Email: "test@example.com",
		},
		Metadata: types.ChunkMetadata{},
	}

	// 存储测试向量
	pointIDs, storeErr := testStorageManager.Qdrant.StoreResumeVectors(
		ctx,
		testUUID,
		[]types.ResumeChunk{testChunk},
		[][]float64{testVector},
	)

	if storeErr != nil {
		t.Logf("保存测试向量失败: %v", storeErr)
	} else if len(pointIDs) > 0 {
		t.Logf("成功保存测试向量，ID: %v", pointIDs)

		// 删除创建的测试向量
		deleteErr := testStorageManager.Qdrant.DeletePoints(ctx, pointIDs)
		if deleteErr != nil {
			t.Logf("删除测试向量失败: %v", deleteErr)
		} else {
			t.Logf("成功删除测试向量")
		}
	}

	t.Logf("Qdrant功能测试完成")
}

// TestRebuildCompleteVectorDatabase 是一个集成测试，用于彻底重建向量数据库
func TestRebuildCompleteVectorDatabase(t *testing.T) {
	// 使用共享测试设置，确保环境配置正确
	oneTimeSetupFunc(t)

	// 跳过CI环境，只在本地运行
	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要的存储组件是否可用
	if testStorageManager.Qdrant == nil {
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 检查MySQL是否可用
	if testStorageManager.MySQL == nil {
		t.Skip("MySQL未配置或不可用，跳过测试")
	}

	// 检查API密钥和嵌入服务
	if testCfg.Aliyun.APIKey == "" || testCfg.Aliyun.APIKey == "your-api-key" {
		t.Skip("未配置有效的阿里云API密钥，跳过测试")
	}

	// 创建上下文
	ctx := context.Background()

	// 0. 先检查Qdrant可用性和当前数据量
	initialCount, err := testStorageManager.Qdrant.CountPoints(ctx)
	if err != nil {
		t.Fatalf("检查Qdrant点数量失败: %v", err)
	}
	t.Logf("开始重建前Qdrant集合中有 %d 个点", initialCount)

	// 1. 从CSV文件加载数据
	chunks, err := loadResumeChunksFromCSV(t, "../../../testdata/resume_submission_chunks.csv")
	require.NoError(t, err, "加载简历块CSV数据失败")

	// 统计不同类型的块数量
	typeCount := make(map[string]int)
	coreTypeCount := make(map[string]int) // 白名单中的核心类型
	totalCount := 0
	totalCoreCount := 0

	for _, chunk := range chunks {
		typeCount[chunk.ChunkType]++
		totalCount++

		// 统计核心类型的数量
		if types.AllowedVectorTypes[types.SectionType(chunk.ChunkType)] {
			coreTypeCount[chunk.ChunkType]++
			totalCoreCount++
		}
	}

	t.Logf("CSV文件中数据块类型统计 (总计: %d 块, 其中核心类型: %d 块):", totalCount, totalCoreCount)
	for chunkType, count := range typeCount {
		isCore := ""
		if types.AllowedVectorTypes[types.SectionType(chunkType)] {
			isCore = " [核心类型]"
		}
		t.Logf("类型 %s: %d 个块%s", chunkType, count, isCore)
	}

	// 按submissionUUID分组处理
	submissionMap := groupChunksBySubmissionUUID(chunks)
	t.Logf("找到 %d 个不同的简历提交需要处理", len(submissionMap))

	// 2. 收集所有存在的point_id，用于后续删除
	var allPointIDs []string
	var submissionPointIDMap = make(map[string][]string) // submissionUUID -> pointIDs

	for _, submissionChunks := range submissionMap {
		for _, chunk := range submissionChunks {
			if chunk.PointID != "" && !strings.HasPrefix(chunk.PointID, "placeholder") {
				allPointIDs = append(allPointIDs, chunk.PointID)
				submissionPointIDMap[chunk.SubmissionUUID] = append(submissionPointIDMap[chunk.SubmissionUUID], chunk.PointID)
			}
		}
	}

	t.Logf("收集到 %d 个已存在的point_id", len(allPointIDs))

	// 3. 先尝试删除向量数据库中所有现有数据
	if len(allPointIDs) > 0 {
		// 分批删除向量，每批次最多处理50个
		batchSize := 50
		totalBatches := (len(allPointIDs) + batchSize - 1) / batchSize // 向上取整计算批次数

		t.Logf("将分 %d 批删除总共 %d 个向量，每批最多 %d 个",
			totalBatches, len(allPointIDs), batchSize)

		for i := 0; i < len(allPointIDs); i += batchSize {
			// 计算当前批次的结束索引
			end := i + batchSize
			if end > len(allPointIDs) {
				end = len(allPointIDs)
			}

			// 获取当前批次的point_id
			batchPointIDs := allPointIDs[i:end]

			// 记录当前批次信息
			t.Logf("删除第 %d/%d 批，包含 %d 个向量",
				(i/batchSize)+1, totalBatches, len(batchPointIDs))

			// 执行删除操作
			deleteErr := testStorageManager.Qdrant.DeletePoints(ctx, batchPointIDs)
			if deleteErr != nil {
				t.Logf("警告: 删除批次 %d 失败: %v，继续执行后续操作",
					(i/batchSize)+1, deleteErr)
			}

			// 添加一个小暂停，避免连续请求过快
			time.Sleep(500 * time.Millisecond)
		}
	}

	// 初始化embedder
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      testCfg.Aliyun.Embedding.Model,
		BaseURL:    testCfg.Aliyun.Embedding.BaseURL,
		Dimensions: testCfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(testCfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	require.NoError(t, err, "初始化embedder失败")

	// 4. 重建向量数据库和更新MySQL中的point_id
	totalChunksProcessed := 0
	totalVectorsCreated := 0

	// 用于记录所有处理的submission的pointIDs
	allSubmissionPointIDs := make(map[string][]string)

	// 使用全局白名单来判断核心块类型，确保与业务代码保持一致
	t.Log("使用白名单过滤核心块类型。当前白名单中的类型：")
	for chunkType, allowed := range types.AllowedVectorTypes {
		if allowed {
			t.Logf("- %s", chunkType)
		}
	}

	for submissionUUID, submissionChunks := range submissionMap {
		t.Logf("处理简历 %s 的 %d 个块", submissionUUID, len(submissionChunks))

		// 获取简历基本信息
		basicInfo, err := getBasicInfoForSubmission(ctx, testStorageManager.MySQL, submissionUUID)
		if err != nil {
			t.Logf("警告: 获取简历 %s 的基本信息失败: %v，使用默认值", submissionUUID, err)
			basicInfo = getDefaultBasicInfo()
		}

		// 准备简历块embedding处理，只选择在白名单中的核心类型
		var selectedChunks []ResumeChunkData
		for _, chunk := range submissionChunks {
			// 检查是否在白名单中
			if types.AllowedVectorTypes[types.SectionType(chunk.ChunkType)] {
				selectedChunks = append(selectedChunks, chunk)
			}
		}

		if len(selectedChunks) == 0 {
			t.Logf("简历 %s 没有任何在白名单中的核心块类型，跳过", submissionUUID)
			continue
		}

		totalChunksProcessed += len(selectedChunks)

		// 准备简历块embedding处理和文本列表
		var resumeChunks []types.ResumeChunk
		var texts []string

		// 为每个块创建ResumeChunk对象和准备嵌入文本
		for _, chunk := range selectedChunks {
			// 创建文本
			embeddingText := fmt.Sprintf("%s: %s", chunk.ChunkTitle, chunk.ChunkContentText)
			texts = append(texts, embeddingText)

			// 创建向量库对象
			resumeChunks = append(resumeChunks, types.ResumeChunk{
				ChunkID:   chunk.ChunkIDInSubmission,
				ChunkType: chunk.ChunkType,
				Content:   chunk.ChunkContentText,
				Type:      types.SectionType(chunk.ChunkType),
				Title:     chunk.ChunkTitle,
				Metadata: map[string]interface{}{
					"submission_uuid": submissionUUID,
					"chunk_type":      chunk.ChunkType,
					"source":          "resume",

					// 添加教育相关元数据
					"highest_education": basicInfo["highest_education"],
					"is_211":            basicInfo["is_211"] == "true",
					"is_985":            basicInfo["is_985"] == "true",
					"is_double_top":     basicInfo["is_double_top"] == "true",

					// 添加经验相关元数据
					"has_work_exp":   basicInfo["has_work_exp"] == "true",
					"has_intern_exp": basicInfo["has_intern"] == "true",

					// 添加奖项相关元数据
					"has_algo_award": basicInfo["has_algorithm_award"] == "true",
					"has_prog_award": basicInfo["has_programming_competition_award"] == "true",

					// content_text字段主要由storage层自动添加，这里为保持元数据完整性也加入
					// 注意：它的值与 resumeChunks 中的 Content 字段相同
					"content_text": chunk.ChunkContentText,
				},
			})
		}

		// 执行embedding
		t.Logf("为简历 %s 执行 %d 个文本嵌入", submissionUUID, len(texts))
		embeddings, err := embedder.EmbedStrings(ctx, texts)
		if err != nil {
			t.Logf("警告: 生成简历 %s 的向量嵌入失败: %v，跳过此简历", submissionUUID, err)
			continue
		}

		// 存储向量到Qdrant
		pointIDs, err := testStorageManager.Qdrant.StoreResumeVectors(ctx, submissionUUID, resumeChunks, embeddings)
		if err != nil {
			t.Logf("警告: 存储简历 %s 的向量到Qdrant失败: %v，跳过此简历", submissionUUID, err)
			continue
		}

		totalVectorsCreated += len(pointIDs)
		t.Logf("简历 %s 的向量已成功存入Qdrant，生成 %d 个point_id", submissionUUID, len(pointIDs))

		// 保存pointIDs以便后续更新MySQL
		allSubmissionPointIDs[submissionUUID] = pointIDs

		// 更新数据库
		err = updatePointIDsInDatabase(ctx, testStorageManager.MySQL, selectedChunks, pointIDs, submissionUUID)
		if err != nil {
			t.Logf("警告: 更新简历 %s 的数据库记录失败: %v", submissionUUID, err)
		} else {
			t.Logf("简历 %s 的数据库记录已成功更新", submissionUUID)
		}

		// 尝试创建或更新简历元数据
		err = updateResumeMetadata(ctx, testStorageManager.MySQL.DB(), submissionUUID, basicInfo)
		if err != nil {
			t.Logf("警告: 保存简历 %s 的元数据失败: %v", submissionUUID, err)
		} else {
			t.Logf("简历 %s 的元数据已成功保存", submissionUUID)
		}

		// 等待一会，避免API限制
		time.Sleep(2 * time.Second)
	}

	// 5. 验证重建结果
	finalCount, err := testStorageManager.Qdrant.CountPoints(ctx)
	if err != nil {
		t.Logf("警告: 重建后检查Qdrant点数量失败: %v", err)
	} else {
		t.Logf("重建完成: Qdrant集合现在有 %d 个点", finalCount)
		t.Logf("处理总结: 处理了 %d 个简历块，创建了 %d 个向量", totalChunksProcessed, totalVectorsCreated)
	}

	// 检查数据一致性
	if int(finalCount) != totalVectorsCreated {
		t.Logf("警告: 向量数据库中的点数 (%d) 与创建的向量数 (%d) 不一致", finalCount, totalVectorsCreated)
	} else {
		t.Logf("数据一致性检查通过: 向量数据库中的点数 (%d) 与创建的向量数 (%d) 一致", finalCount, totalVectorsCreated)
	}

	// 6. 更新resume_submissions表中的qdrant_point_ids字段
	t.Log("更新所有简历的qdrant_point_ids字段...")
	for submissionUUID, pointIDs := range allSubmissionPointIDs {
		if len(pointIDs) > 0 {
			pointIDsJSON, err := json.Marshal(pointIDs)
			if err != nil {
				t.Logf("警告: 序列化简历 %s 的point_ids失败: %v", submissionUUID, err)
				continue
			}

			err = testStorageManager.MySQL.DB().WithContext(ctx).Exec(
				"UPDATE resume_submissions SET qdrant_point_ids = ? WHERE submission_uuid = ?",
				string(pointIDsJSON), submissionUUID,
			).Error

			if err != nil {
				t.Logf("警告: 更新简历 %s 的qdrant_point_ids失败: %v", submissionUUID, err)
			} else {
				t.Logf("简历 %s 的qdrant_point_ids已成功更新", submissionUUID)
			}
		}
	}

	t.Log("向量数据库彻底重建完成!")
}

// updateResumeMetadata 直接更新resume_submissions表中的元数据字段
func updateResumeMetadata(ctx context.Context, db *gorm.DB, submissionUUID string, basicInfo map[string]string) error {
	// 准备更新数据
	updateData := map[string]interface{}{
		// 学校相关布尔字段
		"is_211":            basicInfo["is_211"] == "true",
		"is_985":            basicInfo["is_985"] == "true",
		"is_double_top":     basicInfo["is_double_top"] == "true",
		"highest_education": basicInfo["highest_education"],

		// 经验相关布尔字段
		"has_intern_exp": basicInfo["has_intern"] == "true",
		"has_work_exp":   basicInfo["has_work_exp"] == "true",

		// 奖项相关布尔字段
		"has_algo_award": basicInfo["has_algorithm_award"] == "true",
		"has_prog_award": basicInfo["has_programming_competition_award"] == "true",
	}

	// 处理经验年限
	if expStr, ok := basicInfo["years_of_experience"]; ok && expStr != "" {
		if exp, err := strconv.ParseFloat(expStr, 32); err == nil {
			updateData["years_of_exp"] = exp
		}
	}

	// 准备meta_extra JSON字段数据
	metaExtra := make(map[string]interface{})

	// 添加标签
	if tagsStr, ok := basicInfo["tags"]; ok && tagsStr != "" {
		tagsList := strings.Split(tagsStr, ",")
		metaExtra["tags"] = tagsList
	}

	// 添加算法奖项详情
	if basicInfo["has_algorithm_award"] == "true" && basicInfo["algorithm_award_titles"] != "" {
		var awardTitles []string
		if err := json.Unmarshal([]byte(basicInfo["algorithm_award_titles"]), &awardTitles); err == nil {
			metaExtra["algorithm_award_titles"] = awardTitles
		}
	}

	// 添加编程竞赛奖项详情
	if basicInfo["has_programming_competition_award"] == "true" && basicInfo["programming_competition_titles"] != "" {
		var awardTitles []string
		if err := json.Unmarshal([]byte(basicInfo["programming_competition_titles"]), &awardTitles); err == nil {
			metaExtra["programming_competition_titles"] = awardTitles
		}
	}

	// 添加其他可能的元数据
	if scoreStr, ok := basicInfo["resume_score"]; ok && scoreStr != "" {
		if score, err := strconv.Atoi(scoreStr); err == nil {
			metaExtra["resume_score"] = score
		}
	}

	// 将meta_extra序列化为JSON
	if len(metaExtra) > 0 {
		metaExtraJSON, err := json.Marshal(metaExtra)
		if err == nil {
			updateData["meta_extra"] = string(metaExtraJSON)
		}
	}

	// 执行更新
	result := db.WithContext(ctx).Table("resume_submissions").
		Where("submission_uuid = ?", submissionUUID).
		Updates(updateData)

	return result.Error
}

// TestVerifyPointIDGeneration 测试验证point_id生成的确定性
// 确保相同的输入(resumeID+chunkID)每次都生成相同的point_id
func TestVerifyPointIDGeneration(t *testing.T) {
	// 使用共享测试设置，确保环境配置正确
	oneTimeSetupFunc(t)

	// 跳过CI环境，只在本地运行
	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要的存储组件是否可用
	if testStorageManager.Qdrant == nil {
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 创建上下文
	ctx := context.Background()

	t.Log("开始测试point_id生成的确定性...")

	// 尝试将Qdrant接口转换为具体类型
	vdb := testStorageManager.Qdrant

	// 创建一个测试用的简历ID和chunk
	testResumeID := "test-resume-id-for-deterministic-check"
	testChunks := []types.ResumeChunk{
		{
			ChunkID:         1,
			ChunkType:       "WORK_EXPERIENCE", // 这是白名单允许的类型
			Content:         "这是一个测试内容",
			ImportanceScore: 0.9,
			UniqueIdentifiers: types.Identity{
				Name:  "测试姓名",
				Email: "test@example.com",
				Phone: "12345678901",
			},
			Metadata: types.ChunkMetadata{
				"years_of_experience": 5,
				"highest_education":   "硕士",
			},
		},
		{
			ChunkID:         2,
			ChunkType:       "SKILLS", // 修改为白名单允许的类型
			Content:         "这是另一个技能测试内容",
			ImportanceScore: 0.8,
			UniqueIdentifiers: types.Identity{
				Name:  "测试姓名",
				Email: "test@example.com",
				Phone: "12345678901",
			},
			Metadata: types.ChunkMetadata{
				"years_of_experience": 5,
				"highest_education":   "硕士",
			},
		},
	}

	// 创建测试向量 - 使用固定大小1024
	vectorSize := 1024 // 默认向量大小
	testEmbeddings := [][]float64{
		make([]float64, vectorSize),
		make([]float64, vectorSize),
	}

	// 填充随机值
	for i := range testEmbeddings[0] {
		testEmbeddings[0][i] = rand.Float64()
	}
	for i := range testEmbeddings[1] {
		testEmbeddings[1][i] = rand.Float64()
	}

	// 计算预期会通过白名单过滤的chunks数量
	expectedChunksCount := 0
	for _, chunk := range testChunks {
		// 检查chunk类型是否在白名单中
		if types.AllowedVectorTypes[types.SectionType(chunk.ChunkType)] {
			expectedChunksCount++
		}
	}
	t.Logf("预期通过白名单过滤后的chunks数量: %d/%d", expectedChunksCount, len(testChunks))

	// 第一次生成point_id
	t.Log("第一次生成point_id...")
	pointIDs1, err := vdb.StoreResumeVectors(ctx, testResumeID, testChunks, testEmbeddings)
	require.NoError(t, err, "第一次存储向量失败")
	require.Equal(t, expectedChunksCount, len(pointIDs1), "生成的point_id数量与预期通过白名单的chunks数量不匹配")

	t.Logf("第一次生成的point_ids: %v", pointIDs1)

	// 删除刚生成的向量点，以便于重新测试
	t.Log("删除第一次生成的向量点...")
	deleteErr := testStorageManager.Qdrant.DeletePoints(ctx, pointIDs1)
	if deleteErr != nil {
		t.Logf("警告: 删除向量点失败: %v, 可能会影响后续测试", deleteErr)
	}

	// 重新创建一个向量 (随机填充)
	for i := range testEmbeddings[0] {
		testEmbeddings[0][i] = rand.Float64() // 使用不同的随机值
	}
	for i := range testEmbeddings[1] {
		testEmbeddings[1][i] = rand.Float64() // 使用不同的随机值
	}

	// 第二次生成point_id
	t.Log("第二次生成point_id (使用相同的resumeID和chunkID)...")
	pointIDs2, err := vdb.StoreResumeVectors(ctx, testResumeID, testChunks, testEmbeddings)
	require.NoError(t, err, "第二次存储向量失败")

	// 验证两次生成的point_id是否一致 (确定性检查)
	t.Log("验证两次生成的point_id是否一致...")
	require.Equal(t, expectedChunksCount, len(pointIDs2), "第二次生成的point_id数量与预期不一致")
	require.Equal(t, len(pointIDs1), len(pointIDs2), "两次生成的point_id数量不一致")

	if len(pointIDs1) > 0 {
		for i := 0; i < len(pointIDs1); i++ {
			require.Equal(t, pointIDs1[i], pointIDs2[i],
				fmt.Sprintf("第%d个point_id不一致：第一次=%s，第二次=%s", i+1, pointIDs1[i], pointIDs2[i]))
		}
	} else {
		t.Log("没有生成point_id，跳过一致性检查")
	}

	t.Log("验证通过：相同的resumeID和chunkID生成了相同的point_id")

	// 清理测试生成的数据
	t.Log("清理测试数据...")
	deleteErr = testStorageManager.Qdrant.DeletePoints(ctx, pointIDs2)
	if deleteErr != nil {
		t.Logf("警告: 清理测试数据失败: %v", deleteErr)
	}

	// 只有在有通过白名单的chunk时才进行手动验证
	if expectedChunksCount > 0 {
		// 手动验证内部point_id生成逻辑
		t.Log("手动验证内部point_id生成逻辑...")

		// 这里没法直接访问storage.QdrantPointIDNamespace，因为它不是导出的
		// 所以我们直接使用硬编码的UUID值进行测试
		namespace := uuid.Must(uuid.FromString("fd6c72c2-5a33-4b53-8e7c-8298f3f5a7e1"))

		// 找出实际被向量化的chunks的索引
		vectorizedChunkIndices := []int{}
		for i, chunk := range testChunks {
			if types.AllowedVectorTypes[types.SectionType(chunk.ChunkType)] {
				vectorizedChunkIndices = append(vectorizedChunkIndices, i)
			}
		}

		// 根据实际向量化的chunks生成手动point_id
		var manualPointIDs []string
		for _, idx := range vectorizedChunkIndices {
			pointID := uuid.NewV5(namespace, fmt.Sprintf("resume_id:%s_chunk_id:%d", testResumeID, testChunks[idx].ChunkID)).String()
			manualPointIDs = append(manualPointIDs, pointID)
		}

		t.Logf("手动生成的point_id: %v", manualPointIDs)
		t.Logf("自动生成的point_id: %v", pointIDs1)

		// 验证手动生成的point_id与自动生成的是否一致
		for i := 0; i < len(pointIDs1); i++ {
			require.Equal(t, manualPointIDs[i], pointIDs1[i],
				fmt.Sprintf("手动生成的第%d个point_id与自动生成的不一致", i+1))
		}
	} else {
		t.Log("没有通过白名单的chunk，跳过手动验证point_id生成逻辑")
	}

	t.Log("Point ID生成验证测试完成，生成逻辑确实具有确定性")
}
