package handler_test

import (
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/outbox"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"ai-agent-go/pkg/utils"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testResumeUUID1 = "1a7e6ea6-1743-4200-a337-14365b2e3532"
	testResumeUUID2 = "c5b2a1a8-2943-4f3b-8f32-3a5e8d6e1b43"
)

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
