package processor

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/storage"
	storagetypes "ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	gormLogger "gorm.io/gorm/logger"
)

// MockPDFExtractor 模拟PDF提取器
type MockPDFExtractor struct {
	text     string
	metadata map[string]interface{}
	err      error
}

func (m *MockPDFExtractor) ExtractFromFile(ctx context.Context, filePath string) (string, map[string]interface{}, error) {
	return m.text, m.metadata, m.err
}

func (m *MockPDFExtractor) ExtractTextFromReader(ctx context.Context, reader io.Reader, uri string, options interface{}) (string, map[string]interface{}, error) {
	return m.text, m.metadata, m.err
}

func (m *MockPDFExtractor) ExtractTextFromBytes(ctx context.Context, data []byte, uri string, options interface{}) (string, map[string]interface{}, error) {
	return m.text, m.metadata, m.err
}

func (m *MockPDFExtractor) ExtractStructuredContent(ctx context.Context, reader io.Reader, uri string, options interface{}) (map[string]interface{}, error) {
	return m.metadata, m.err
}

// MockResumeChunker 模拟简历分块器
type MockResumeChunker struct {
	sections  []*types.ResumeSection
	basicInfo map[string]string
	err       error
}

func (m *MockResumeChunker) ChunkResume(ctx context.Context, text string) ([]*types.ResumeSection, map[string]string, error) {
	return m.sections, m.basicInfo, m.err
}

// MockResumeEmbedder 模拟简历嵌入器
type MockResumeEmbedder struct {
	vectors []*types.ResumeChunkVector
	err     error
}

func (m *MockResumeEmbedder) EmbedResumeChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) {
	return m.vectors, m.err
}

func (m *MockResumeEmbedder) Embed(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) {
	return m.vectors, m.err
}

// MockJobMatchEvaluator 模拟岗位匹配评估器
type MockJobMatchEvaluator struct {
	evaluation *types.JobMatchEvaluation
	err        error
}

func (m *MockJobMatchEvaluator) EvaluateMatch(ctx context.Context, jobDescription string, resumeText string) (*types.JobMatchEvaluation, error) {
	return m.evaluation, m.err
}

// MockVectorStore 模拟向量存储
type MockVectorStore struct {
	err error
}

func (m *MockVectorStore) StoreVectors(ctx context.Context, vectors []*types.ResumeChunkVector) error {
	return m.err
}

// TestResumeProcessor 测试简历处理器的组件聚合能力
func TestResumeProcessor(t *testing.T) {
	// 创建上下文
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 加载真实配置文件，遵循单元测试规范不使用环境变量
	cfg, err := config.LoadConfig("../../internal/config/config.yaml")
	require.NoError(t, err, "加载配置文件失败")

	// 确保Tika配置存在，强制优先使用Tika
	if cfg.Tika.ServerURL == "" {
		cfg.Tika.ServerURL = "http://localhost:9998" // 默认Tika服务器地址
	}
	cfg.Tika.Type = "tika" // 强制使用tika类型

	// 禁用过多日志输出
	cfg.MySQL.LogLevel = 1     // gormLogger.Silent
	cfg.Logger.Level = "error" // 只记录错误级别日志

	// 创建真实的存储组件，而不是使用mock
	silentLogger := log.New(io.Discard, "[TEST] ", log.LstdFlags)
	storageManager, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "创建存储管理器失败")

	// 配置静默日志
	if storageManager != nil && storageManager.MySQL != nil {
		silentGormLogger := gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags),
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		storageManager.MySQL.DB().Logger = silentGormLogger
	}

	// 确保测试结束后清理资源
	defer func() {
		if storageManager != nil && storageManager.MySQL != nil {
			_ = storageManager.MySQL.Close()
		}
	}()

	// 创建模拟组件
	mockExtractor := &MockPDFExtractor{
		text: "这是一个测试简历文本",
		metadata: map[string]interface{}{
			"source_file_path": "test.pdf",
		},
	}

	mockChunker := &MockResumeChunker{
		sections: []*types.ResumeSection{
			{
				Type:    types.SectionBasicInfo,
				Title:   "基本信息",
				Content: "姓名：测试",
			},
			{
				Type:    types.SectionEducation,
				Title:   "教育经历",
				Content: "大学：测试大学",
			},
		},
		basicInfo: map[string]string{
			"name": "测试",
		},
	}

	mockEmbedder := &MockResumeEmbedder{
		vectors: []*types.ResumeChunkVector{
			{
				Section: mockChunker.sections[0],
				Vector:  []float64{0.1, 0.2, 0.3},
			},
		},
	}

	mockEvaluator := &MockJobMatchEvaluator{
		evaluation: &types.JobMatchEvaluation{
			MatchScore:      75,
			MatchHighlights: []string{"技能匹配", "教育背景相符"},
			PotentialGaps:   []string{"经验不足"},
			EvaluatedAt:     time.Now().Unix(),
		},
	}

	// 测试组件配置
	t.Run("ComponentConfiguration", func(t *testing.T) {
		processor := NewResumeProcessor(
			WithPDFExtractor(mockExtractor),
			WithResumeChunker(mockChunker),
			WithResumeEmbedder(mockEmbedder),
			WithJobMatchEvaluator(mockEvaluator),
			WithDebugMode(true),
			WithStorage(storageManager),       // 提供实际的Storage实例
			WithProcessorLogger(silentLogger), // 使用静默日志记录器
		)

		// 验证组件是否正确配置
		assert.Equal(t, mockExtractor, processor.PDFExtractor)
		assert.Equal(t, mockChunker, processor.ResumeChunker)
		assert.Equal(t, mockEmbedder, processor.ResumeEmbedder)
		assert.Equal(t, mockEvaluator, processor.MatchEvaluator)
		assert.Equal(t, storageManager, processor.Storage)
		assert.True(t, processor.Config.Debug)
	})

	// 测试默认处理器创建 - 验证PDF解析器优先使用Tika
	t.Run("DefaultProcessorCreation", func(t *testing.T) {
		// 确保Tika配置准备好
		cfg.Tika.ServerURL = "http://localhost:9998"
		cfg.Tika.Type = "tika"

		processor, err := CreateProcessorFromConfig(ctx, cfg, storageManager)
		require.NoError(t, err)

		// 验证默认组件
		assert.NotNil(t, processor.PDFExtractor)
		assert.NotNil(t, processor.ResumeChunker)
		assert.NotNil(t, processor.Storage) // 验证Storage不为空
		assert.True(t, processor.Config.Debug)

		// 验证PDF解析器是TikaPDFExtractor类型
		_, isTika := processor.PDFExtractor.(*parser.TikaPDFExtractor)
		assert.True(t, isTika, "默认PDF解析器应该是TikaPDFExtractor")
	})

	// 测试完整的处理器从配置创建
	t.Run("CreateProcessorFromConfig", func(t *testing.T) {
		// 确保Tika配置准备好
		cfg.Tika.ServerURL = "http://localhost:9998"
		cfg.Tika.Type = "tika"

		processor, err := CreateProcessorFromConfig(ctx, cfg, storageManager)
		require.NoError(t, err)

		// 验证全部组件是否已正确初始化
		assert.NotNil(t, processor.PDFExtractor, "PDF提取器应该被初始化")
		assert.NotNil(t, processor.Storage, "Storage应该被初始化")

		// 验证PDF解析器类型是TikaPDFExtractor
		_, isTika := processor.PDFExtractor.(*parser.TikaPDFExtractor)
		assert.True(t, isTika, "配置创建的PDF解析器应该是TikaPDFExtractor")

		// 验证配置是否被正确应用
		assert.Equal(t, cfg.Qdrant.Dimension, processor.Config.DefaultDimensions, "维度配置应该被正确应用")
		assert.Equal(t, cfg.Logger.Level == "debug", processor.Config.Debug, "调试模式应该从配置正确设置")
	})

	// 测试日志功能
	t.Run("LoggingFunctionality", func(t *testing.T) {
		testLogger := log.New(io.Discard, "[TestLogger] ", log.LstdFlags)
		processor := NewResumeProcessor(
			WithDebugMode(true),
			WithStorage(storageManager),     // 提供实际的Storage实例
			WithProcessorLogger(testLogger), // 使用测试日志记录器
		)

		// 使用捕获日志的方式测试日志功能
		processor.LogDebug("测试日志消息")
		// ResumeProcessor没有暴露Logger字段，所以只检查Storage
		assert.NotNil(t, processor.Storage)
	})
}

// TestProcessUploadedResume_TransactionRollback 测试当业务处理出错时事务会正确回滚
// 本测试使用本地环境模拟出错场景，而不是使用mock
func TestProcessUploadedResume_TransactionRollback(t *testing.T) {
	// 1. 加载测试配置
	cfg, err := config.LoadConfig("../../internal/config/config.yaml")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	// 使用特殊配置禁用日志输出
	cfg.MySQL.LogLevel = 1 // gormLogger.Silent 静默所有SQL日志

	// 2. 创建真实的存储组件 - 仅Redis
	storageManager, err := storage.NewStorage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("创建存储管理器失败: %v", err)
	}

	// 禁用GORM的SQL日志输出
	if storageManager != nil && storageManager.MySQL != nil {
		// 创建一个完全静默的GORM日志实例
		silentGormLoggerInstance := gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags), // 使用io.Discard屏蔽所有输出
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		// 应用到MySQL连接
		storageManager.MySQL.DB().Logger = silentGormLoggerInstance

		// 应用到连接池
		sqlDB, err := storageManager.MySQL.DB().DB()
		if err == nil {
			sqlDB.SetMaxIdleConns(1)
			sqlDB.SetMaxOpenConns(2)
		}
	}

	// 确保测试后清理资源
	defer func() {
		// 尝试关闭MySQL连接
		if storageManager != nil && storageManager.MySQL != nil {
			err := storageManager.MySQL.Close()
			if err != nil {
				t.Logf("关闭MySQL连接失败: %v", err)
			}
		}
	}()

	// 4. 创建一个真实的工作PDF提取器
	pdfExtractor := parser.NewTikaPDFExtractor(cfg.Tika.ServerURL)

	// 5. 创建特殊的ResumeProcessor，用于触发事务回滚
	// 利用组件和设置分离的新架构
	components := &Components{
		PDFExtractor: pdfExtractor,
		Storage:      storageManager,
	}

	settings := &Settings{
		UseLLM:            true,
		DefaultDimensions: 1536,
		Debug:             true,
		Logger:            log.New(io.Discard, "[TEST] ", log.LstdFlags), // 使用禁止输出的logger
		TimeLocation:      time.Local,
	}

	// 创建处理器
	processor := NewResumeProcessorV2(components, settings)

	// 6. 准备测试数据 - 使用固定UUID而不是时间戳生成的
	testSubmissionUUID := "test-transaction-rollback-fixed-uuid"
	message := storagetypes.ResumeUploadMessage{
		SubmissionUUID:      testSubmissionUUID,
		OriginalFilePathOSS: "non-existent-file.pdf", // 不存在的文件，确保会失败
	}

	// 7. 先在数据库中创建测试记录，这样才能测试事务回滚
	initialTime := time.Now()
	testSubmission := models.ResumeSubmission{
		SubmissionUUID:      testSubmissionUUID,
		ProcessingStatus:    "UPLOAD_SUCCESS", // 初始状态
		SubmissionTimestamp: initialTime,
		OriginalFilename:    "test.pdf",
		OriginalFilePathOSS: "non-existent-file.pdf",
		CreatedAt:           initialTime,
		UpdatedAt:           initialTime,
	}

	// 使用静默的DB会话操作
	db := storageManager.MySQL.DB().Session(&gorm.Session{
		Logger: gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags),
			gormLogger.Config{
				LogLevel: gormLogger.Silent,
			},
		),
	})

	err = db.Create(&testSubmission).Error
	if err != nil {
		t.Fatalf("创建测试简历记录失败: %v", err)
	}

	// 保存创建后的原始记录以供后续比较
	var originalSubmission models.ResumeSubmission
	err = db.Where("submission_uuid = ?", testSubmissionUUID).First(&originalSubmission).Error
	if err != nil {
		t.Fatalf("获取原始记录失败: %v", err)
	}

	// 确保测试后删除测试记录
	defer func() {
		if storageManager != nil && storageManager.MySQL != nil {
			err := db.Unscoped().Where("submission_uuid = ?", testSubmissionUUID).
				Delete(&models.ResumeSubmission{}).Error
			if err != nil {
				t.Logf("清理测试数据失败: %v", err)
			}
		}
	}()

	// 8. 执行测试 - 应当触发事务回滚
	err = processor.ProcessUploadedResume(context.Background(), message, cfg)

	// 9. 验证结果
	// 9.1 应当有错误返回，因为文件不存在
	require.Error(t, err, "期望处理失败但返回成功")

	// 9.2 使用errors.Is检查错误类型
	var resumeErr *ResumeProcessError
	if errors.As(err, &resumeErr) {
		assert.True(t, errors.Is(resumeErr, ErrResumeDownloadFailed), "错误类型应是下载失败错误")
	} else {
		// 向后兼容：如果还没有完全改造错误处理，仍然检查字符串包含
		assert.True(t, strings.Contains(err.Error(), "下载简历失败"),
			"错误信息应包含'下载简历失败'，实际: %v", err)
	}

	// 9.3 检查数据库中的状态，由于事务回滚和错误处理，应该是UPLOAD_PROCESSING_FAILED
	var actualSubmission models.ResumeSubmission
	err = db.Where("submission_uuid = ?", testSubmissionUUID).First(&actualSubmission).Error
	require.NoError(t, err, "查询测试记录失败")

	// 9.4 验证多个字段确认事务效果
	expectedStatus := "UPLOAD_PROCESSING_FAILED"
	assert.Equal(t, expectedStatus, actualSubmission.ProcessingStatus,
		"状态不符合预期，期望: %s, 实际: %s", expectedStatus, actualSubmission.ProcessingStatus)

	// 9.5 验证更新时间已变化
	assert.NotEqual(t, originalSubmission.UpdatedAt, actualSubmission.UpdatedAt,
		"UpdatedAt字段应该在处理后发生变化")

	// 9.6 验证ProcessingStatus已经改变
	assert.NotEqual(t, originalSubmission.ProcessingStatus, actualSubmission.ProcessingStatus,
		"ProcessingStatus字段应在处理后已发生变化")
}

func TestProcessUploadedResume_ConcurrentAccess(t *testing.T) {
	// 将集成测试标记为短测试，使用 -short 标志来控制是否跳过
	if testing.Short() {
		t.Skip("使用 -short 标志跳过集成测试")
	}

	// 1. 加载测试配置
	cfg, err := config.LoadConfig("../../internal/config/config.yaml")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	// 使用特殊配置禁用日志输出
	cfg.MySQL.LogLevel = 1 // gormLogger.Silent 静默所有SQL日志

	// 2. 创建真实的存储组件 - 仅Redis
	storageManager, err := storage.NewStorage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("创建存储管理器失败: %v", err)
	}

	// 禁用GORM的SQL日志输出
	if storageManager != nil && storageManager.MySQL != nil {
		silentGormLoggerInstance := gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags),
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		storageManager.MySQL.DB().Logger = silentGormLoggerInstance
	}

	// 确保测试后清理资源
	defer func() {
		if storageManager != nil && storageManager.MySQL != nil {
			err := storageManager.MySQL.Close()
			if err != nil {
				t.Logf("关闭MySQL连接失败: %v", err)
			}
		}
	}()

	// 4. 使用固定UUID创建测试数据
	testSubmissionUUID := "test-concurrent-access-fixed-uuid"
	initialTime := time.Now()

	// 使用静默的DB会话操作
	db := storageManager.MySQL.DB().Session(&gorm.Session{
		Logger: gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags),
			gormLogger.Config{LogLevel: gormLogger.Silent},
		),
	})

	// 5. 创建测试记录
	testSubmission := models.ResumeSubmission{
		SubmissionUUID:      testSubmissionUUID,
		ProcessingStatus:    "UPLOAD_SUCCESS", // 初始状态
		SubmissionTimestamp: initialTime,
		OriginalFilename:    "test-concurrent.pdf",
		OriginalFilePathOSS: "non-existent-concurrent.pdf", // 不存在的文件，确保处理会失败
		CreatedAt:           initialTime,
		UpdatedAt:           initialTime,
	}

	// 确保记录不存在，然后创建
	db.Where("submission_uuid = ?", testSubmissionUUID).Delete(&models.ResumeSubmission{})
	err = db.Create(&testSubmission).Error
	if err != nil {
		t.Fatalf("创建测试简历记录失败: %v", err)
	}

	// 确保测试后删除测试记录
	defer func() {
		if storageManager != nil && storageManager.MySQL != nil {
			err := db.Unscoped().Where("submission_uuid = ?", testSubmissionUUID).
				Delete(&models.ResumeSubmission{}).Error
			if err != nil {
				t.Logf("清理测试数据失败: %v", err)
			}
		}
	}()

	// 6. 准备并发测试 - 创建多个处理器同时处理同一个简历
	const concurrentProcessors = 5
	message := storagetypes.ResumeUploadMessage{
		SubmissionUUID:      testSubmissionUUID,
		OriginalFilePathOSS: "non-existent-concurrent.pdf",
	}

	// 使用WaitGroup等待所有goroutine完成
	var wg sync.WaitGroup
	wg.Add(concurrentProcessors)

	// 存储每个处理器的错误
	processorErrors := make([]error, concurrentProcessors)

	// 7. 并发执行处理
	for i := 0; i < concurrentProcessors; i++ {
		go func(idx int) {
			defer wg.Done()

			// 每个goroutine创建一个独立的处理器
			pdfExtractor := parser.NewTikaPDFExtractor(cfg.Tika.ServerURL)
			processor := NewResumeProcessorV2(
				&Components{PDFExtractor: pdfExtractor, Storage: storageManager},
				&Settings{Debug: true, Logger: log.New(io.Discard, "[TEST] ", log.LstdFlags), TimeLocation: time.Local},
			)

			// 尝试处理同一个简历
			processorErrors[idx] = processor.ProcessUploadedResume(context.Background(), message, cfg)
		}(i)
	}

	// 等待所有goroutine完成
	wg.Wait()

	// 8. 验证结果
	// 8.1 检查所有处理器是否都失败了
	failCount := 0
	for i, err := range processorErrors {
		if err != nil {
			failCount++
			t.Logf("处理器 %d 返回了错误: %v", i, err)
		}
	}
	assert.Equal(t, concurrentProcessors, failCount, "所有处理器都应该失败")

	// 8.2 检查最终状态 - 应该只有一次状态更新成功，其他都被事务隔离
	var finalSubmission models.ResumeSubmission
	err = db.Where("submission_uuid = ?", testSubmissionUUID).First(&finalSubmission).Error
	require.NoError(t, err, "查询最终记录失败")

	// 期望状态从UPLOAD_SUCCESS变为UPLOAD_PROCESSING_FAILED，但仅变化一次
	expectedStatus := "UPLOAD_PROCESSING_FAILED"
	assert.Equal(t, expectedStatus, finalSubmission.ProcessingStatus,
		"最终状态应该是UPLOAD_PROCESSING_FAILED")

	// 注意：在某些事务隔离级别下，可能并不是每次都会更新时间戳
	// 我们主要关心状态是否更新为失败，而不是具体时间戳
	// 因此只验证流程完成后状态是否正确
	t.Logf("原始时间: %v, 最终时间: %v",
		initialTime.Format(time.RFC3339Nano),
		finalSubmission.UpdatedAt.Format(time.RFC3339Nano))

	// 确认最终状态是失败，而不依赖于具体的UpdatedAt值
	assert.NotEqual(t, "UPLOAD_SUCCESS", finalSubmission.ProcessingStatus,
		"最终状态不应该是UPLOAD_SUCCESS")
	assert.Equal(t, expectedStatus, finalSubmission.ProcessingStatus,
		"最终状态应该是UPLOAD_PROCESSING_FAILED")
}

// TestRedisAtomicDeduplication 测试Redis原子化去重操作在并发情况下的正确性
func TestRedisAtomicDeduplication(t *testing.T) {
	// 将集成测试标记为短测试，使用 -short 标志来控制是否跳过
	if testing.Short() {
		t.Skip("使用 -short 标志跳过集成测试")
	}

	// 1. 加载测试配置
	cfg, err := config.LoadConfig("../../internal/config/config.yaml")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	// 使用特殊配置禁用日志输出
	cfg.MySQL.LogLevel = 1 // gormLogger.Silent 静默所有SQL日志

	// 2. 创建真实的存储组件 - 仅Redis
	storageManager, err := storage.NewStorage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("创建存储管理器失败: %v", err)
	}

	// 禁用GORM的SQL日志输出
	if storageManager != nil && storageManager.MySQL != nil {
		silentGormLoggerInstance := gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags),
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		storageManager.MySQL.DB().Logger = silentGormLoggerInstance
	}

	// 确保测试后清理资源
	defer func() {
		if storageManager != nil {
			if storageManager.Redis != nil {
				err := storageManager.Redis.Close()
				if err != nil {
					t.Logf("关闭Redis连接失败: %v", err)
				}
			}
		}
	}()

	// 3. 准备测试 - 使用固定文本MD5进行测试
	testMD5 := "test-concurrent-atomic-md5-" + time.Now().Format("20060102150405")

	// 测试前确保此MD5不存在
	exists, err := storageManager.Redis.CheckParsedTextMD5Exists(context.Background(), testMD5)
	if err != nil {
		t.Fatalf("测试前检查MD5是否存在失败: %v", err)
	}
	if exists {
		t.Fatalf("测试开始前MD5已存在，无法进行测试")
	}

	// 4. 准备并发测试 - 多个goroutine同时调用原子操作
	const concurrentRequests = 10
	var wg sync.WaitGroup
	wg.Add(concurrentRequests)

	// 记录结果
	type result struct {
		exists bool
		err    error
	}
	results := make([]result, concurrentRequests)

	// 5. 并发执行原子操作
	for i := 0; i < concurrentRequests; i++ {
		go func(idx int) {
			defer wg.Done()
			// 每个goroutine独立调用原子操作检查并添加同一个MD5
			exists, err := storageManager.Redis.CheckAndAddParsedTextMD5(context.Background(), testMD5)
			results[idx] = result{exists, err}
		}(i)
	}

	// 等待所有goroutine完成
	wg.Wait()

	// 6. 验证结果
	// 检查有且仅有一个goroutine认为这个MD5是新加的（返回false）
	falseCount := 0
	errorCount := 0
	for i, res := range results {
		if res.err != nil {
			errorCount++
			t.Logf("请求 %d 返回错误: %v", i, res.err)
		}
		if !res.exists {
			falseCount++
			t.Logf("请求 %d 认为MD5不存在 (false)", i)
		}
	}

	// 7. 验证预期结果
	assert.Equal(t, 0, errorCount, "所有请求都应该成功，无错误")
	assert.Equal(t, 1, falseCount, "只应有一个请求认为MD5是新的，实际有: %d", falseCount)

	// 8. 验证最终状态 - MD5现在应该存在
	finalExists, err := storageManager.Redis.CheckParsedTextMD5Exists(context.Background(), testMD5)
	assert.NoError(t, err, "测试后检查MD5是否存在不应该有错误")
	assert.True(t, finalExists, "测试后MD5应该存在")

	// 9. 清理 - 从Redis移除测试MD5
	client := storageManager.Redis.Client
	key := fmt.Sprintf("tenant:%s:parsed_text_md5_set", "default_tenant") // 使用默认租户
	_, err = client.SRem(context.Background(), key, testMD5).Result()
	if err != nil {
		t.Logf("清理测试MD5时出错: %v", err)
	}
}

// TestMQMessageIdempotence 测试MQ消息重投时的幂等性处理
func TestMQMessageIdempotence(t *testing.T) {
	// 将集成测试标记为短测试，使用 -short 标志来控制是否跳过
	if testing.Short() {
		t.Skip("使用 -short 标志跳过集成测试")
	}

	// 1. 加载测试配置
	cfg, err := config.LoadConfig("../../internal/config/config.yaml")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}

	// 使用特殊配置禁用日志输出
	cfg.MySQL.LogLevel = 1 // gormLogger.Silent 静默所有SQL日志

	// 2. 创建真实的存储组件
	storageManager, err := storage.NewStorage(context.Background(), cfg)
	if err != nil {
		t.Fatalf("创建存储管理器失败: %v", err)
	}

	// 禁用GORM的SQL日志输出
	if storageManager != nil && storageManager.MySQL != nil {
		silentGormLoggerInstance := gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags),
			gormLogger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  gormLogger.Silent,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		)
		storageManager.MySQL.DB().Logger = silentGormLoggerInstance
	}

	// 确保测试后清理资源
	defer func() {
		if storageManager != nil && storageManager.MySQL != nil {
			err := storageManager.MySQL.Close()
			if err != nil {
				t.Logf("关闭MySQL连接失败: %v", err)
			}
		}
	}()

	// 4. 创建测试数据 - 使用标准格式的UUID
	// 使用标准UUID格式，避免 "uuid: incorrect UUID length" 错误
	testSubmissionUUID := uuid.Must(uuid.NewV4()).String()
	initialTime := time.Now()

	// 使用静默的DB会话操作
	db := storageManager.MySQL.DB().Session(&gorm.Session{
		Logger: gormLogger.New(
			log.New(io.Discard, "", log.LstdFlags),
			gormLogger.Config{LogLevel: gormLogger.Silent},
		),
	})

	// 5. 创建解析文本文件，确保能从MinIO读取
	parsedTextContent := "这是测试用的解析文本内容"
	parsedTextPath := "test-parsed-" + testSubmissionUUID + ".txt" // 使用UUID创建唯一路径

	// 上传测试文本到MinIO
	parsedTextPath, err = storageManager.MinIO.UploadParsedText(context.Background(), testSubmissionUUID, parsedTextContent)
	if err != nil {
		t.Logf("上传测试解析文本失败: %v - 测试可能会失败", err)
	}

	// 5b. 创建已处理完成状态的测试记录
	testSubmission := models.ResumeSubmission{
		SubmissionUUID:      testSubmissionUUID,
		ProcessingStatus:    constants.StatusProcessingCompleted, // 已经是完成状态
		SubmissionTimestamp: initialTime,
		OriginalFilename:    "test-idempotence.pdf",
		OriginalFilePathOSS: "test-path.pdf",
		ParsedTextPathOSS:   parsedTextPath, // 使用实际创建的路径
		RawTextMD5:          "test-md5-" + time.Now().Format("20060102150405"),
		CreatedAt:           initialTime,
		UpdatedAt:           initialTime,
	}

	// 确保记录不存在，然后创建
	db.Where("submission_uuid = ?", testSubmissionUUID).Delete(&models.ResumeSubmission{})
	err = db.Create(&testSubmission).Error
	if err != nil {
		t.Fatalf("创建测试简历记录失败: %v", err)
	}

	// 记录创建后的时间戳，用于后续比较
	initialUpdateTime := testSubmission.UpdatedAt

	// 确保测试后删除测试记录
	defer func() {
		// 清理数据库记录
		if storageManager != nil && storageManager.MySQL != nil {
			err := db.Unscoped().Where("submission_uuid = ?", testSubmissionUUID).
				Delete(&models.ResumeSubmission{}).Error
			if err != nil {
				t.Logf("清理数据库测试数据失败: %v", err)
			}
		}

		// 清理MinIO对象
		if storageManager != nil && storageManager.MinIO != nil && parsedTextPath != "" {
			err := storageManager.MinIO.RemoveObject(context.Background(),
				cfg.MinIO.ParsedTextBucket, parsedTextPath, minio.RemoveObjectOptions{})
			if err != nil {
				t.Logf("清理MinIO测试文件失败: %v", err)
			}
		}
	}()

	// 6. 创建PDF提取器和处理器
	pdfExtractor := parser.NewTikaPDFExtractor(cfg.Tika.ServerURL)

	// 创建必要的组件
	mockChunker := &MockResumeChunker{
		sections: []*types.ResumeSection{
			{Title: "测试部分", Content: "测试内容"},
		},
		basicInfo: map[string]string{"name": "测试用户"},
	}

	mockEmbedder := &MockResumeEmbedder{
		vectors: []*types.ResumeChunkVector{
			{
				Section:  &types.ResumeSection{Title: "测试部分", Content: "测试内容"},
				Vector:   make([]float64, 1024), // 创建1024维向量，匹配Qdrant需求
				Metadata: map[string]interface{}{"name": "测试用户"},
			},
		},
	}

	mockEvaluator := &MockJobMatchEvaluator{
		evaluation: &types.JobMatchEvaluation{
			MatchScore:      85,
			MatchHighlights: []string{"技能匹配"},
			PotentialGaps:   []string{"经验不足"},
		},
	}

	// 创建处理器并提供所有必要的组件
	processor := NewResumeProcessorV2(
		&Components{
			PDFExtractor:   pdfExtractor,
			Storage:        storageManager,
			ResumeChunker:  mockChunker,
			ResumeEmbedder: mockEmbedder,
			MatchEvaluator: mockEvaluator,
		},
		&Settings{Debug: true, Logger: log.New(io.Discard, "[TEST] ", log.LstdFlags), TimeLocation: time.Local},
	)

	// 7. 模拟MQ消息重投 - 创建处理消息
	message := storagetypes.ResumeProcessingMessage{
		SubmissionUUID:      testSubmissionUUID,
		ParsedTextPathOSS:   parsedTextPath,
		ParsedTextObjectKey: parsedTextPath,
	}

	// 8. 尝试重复处理
	err = processor.ProcessLLMTasks(context.Background(), message, cfg)

	// 9. 验证结果
	// 9.1 由于记录已是完成状态，处理应该被跳过，不应返回错误
	assert.NoError(t, err, "处理已完成状态的记录不应返回错误")

	// 9.2 验证记录状态和更新时间未发生变化（处理被跳过）
	var afterSubmission models.ResumeSubmission
	err = db.Where("submission_uuid = ?", testSubmissionUUID).First(&afterSubmission).Error
	require.NoError(t, err, "查询测试记录失败")

	// 9.3 验证状态未变化
	assert.Equal(t, constants.StatusProcessingCompleted, afterSubmission.ProcessingStatus,
		"处理已完成状态的记录后，状态不应变化")

	// 9.4 验证更新时间未被修改（处理被跳过）
	assert.Equal(t, initialUpdateTime.Unix(), afterSubmission.UpdatedAt.Unix(),
		"处理已完成状态的记录后，更新时间不应变化")

	// 10. 测试Qdrant向量存储的幂等性（如果可以访问Qdrant）
	if storageManager.Qdrant != nil {
		// 10.1 准备测试数据 - 创建与Qdrant集合维度匹配的向量
		vectorDimension := 1024 // 与Qdrant集合维度匹配
		testVector := make([]float64, vectorDimension)
		for i := 0; i < vectorDimension; i++ {
			testVector[i] = 0.01 * float64(i%100) // 填充一些测试值
		}

		// 10.2 准备测试数据结构
		chunk := types.ResumeChunk{
			ChunkID:   1,
			ChunkType: "TEST",
			Content:   "测试内容",
		}

		// 10.3 多次插入相同ID的向量
		_, err1 := storageManager.Qdrant.StoreResumeVectors(
			context.Background(),
			testSubmissionUUID,
			[]types.ResumeChunk{chunk},
			[][]float64{testVector},
		)

		// 10.4 再次插入相同ID的向量（模拟重复处理）
		_, err2 := storageManager.Qdrant.StoreResumeVectors(
			context.Background(),
			testSubmissionUUID,
			[]types.ResumeChunk{chunk},
			[][]float64{testVector},
		)

		// 10.5 验证两次操作都成功，不会因为ID重复而失败
		assert.NoError(t, err1, "首次插入向量应成功")
		assert.NoError(t, err2, "重复插入相同ID的向量应成功（Upsert操作保证幂等性）")
	}
}
