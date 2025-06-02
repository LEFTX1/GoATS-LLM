package handler

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/logger"
	"ai-agent-go/internal/processor"
	storage2 "ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"ai-agent-go/pkg/utils"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	// 导入共享类型
	"github.com/gofrs/uuid/v5" // For redis.Nil
	"gorm.io/gorm"
)

// ResumeHandler 简历处理器，负责协调简历的处理流程
type ResumeHandler struct {
	cfg             *config.Config
	storage         *storage2.Storage          // 使用聚合的storage实例替换独立的适配器
	processorModule *processor.ResumeProcessor // 使用组件聚合类
}

// NewResumeHandler 创建一个新的简历处理器
func NewResumeHandler(
	cfg *config.Config,
	storage *storage2.Storage, // 接收聚合的storage实例
	processorModule *processor.ResumeProcessor, // 只接收组件聚合类
) *ResumeHandler {
	return &ResumeHandler{
		cfg:             cfg,
		storage:         storage, // 初始化聚合的storage实例
		processorModule: processorModule,
	}
}

// ResumeUploadResponse 简历上传响应
type ResumeUploadResponse struct {
	SubmissionUUID string `json:"submission_uuid"`
	Status         string `json:"status"`
}

// HandleResumeUpload 处理简历上传请求
func (h *ResumeHandler) HandleResumeUpload(ctx context.Context, reader io.Reader, fileSize int64,
	filename string, targetJobID string, sourceChannel string) (*ResumeUploadResponse, error) {

	// 0. 读取文件内容并计算文件本身的MD5 (需要在上传MinIO前，且reader只能读一次)
	fileBytes, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("读取上传文件内容失败: %w", err)
	}
	fileMD5Hex := utils.CalculateMD5(fileBytes)

	// 检查文件MD5是否已存在于Redis Set
	exists, err := h.storage.Redis.CheckRawFileMD5Exists(ctx, fileMD5Hex)
	if err != nil {
		// 如果Redis查询失败，根据策略决定是继续还是报错。这里选择报错，因为去重是重要逻辑。
		logger.Error().
			Err(err).
			Str("md5", fileMD5Hex).
			Msg("查询Redis文件MD5 Set失败")
		return nil, fmt.Errorf("检查文件MD5重复性时Redis查询失败: %w", err)
	}

	if exists {
		logger.Info().
			Str("md5", fileMD5Hex).
			Str("filename", filename).
			Msg("检测到重复的文件MD5，跳过处理")
		return &ResumeUploadResponse{
			SubmissionUUID: "", // 或者可以考虑生成一个UUID并记录为DUPLICATE_FILE状态，但不触发后续
			Status:         "DUPLICATE_FILE_SKIPPED",
		}, nil
	}

	// 1. 生成UUIDv7
	uuidV7, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("生成UUIDv7失败: %w", err)
	}
	submissionUUID := uuidV7.String()

	// 2. 获取文件扩展名
	ext := filepath.Ext(filename)
	if ext == "" {
		ext = ".pdf" // 默认为PDF
	}

	// 3. 上传原始文件到MinIO
	// 因为 fileBytes 已经被读取，需要用 bytes.NewReader 重新包装
	originalObjectKey, err := h.storage.MinIO.UploadResumeFile(ctx, submissionUUID, ext, bytes.NewReader(fileBytes), int64(len(fileBytes)))
	if err != nil {
		return nil, fmt.Errorf("上传简历到MinIO失败: %w", err)
	}

	// 在MinIO上传成功后，将新的文件MD5添加到Redis Set，并设置过期时间
	if err := h.storage.Redis.AddRawFileMD5(ctx, fileMD5Hex); err != nil {
		// 如果添加到Redis Set失败，这是一个潜在的数据不一致问题。
		// 根据策略，可以选择：
		// 1. 强一致性：删除已上传的MinIO对象，然后返回错误。（较复杂）
		// 2. 容忍：记录错误，继续流程。下次相同文件会再次通过MD5检查，但如果这次Redis Set添加一直失败，去重会失效。（当前选择）
		// 3. 尝试重试添加到Redis。
		logger.Warn().
			Err(err).
			Str("md5", fileMD5Hex).
			Str("object_key", originalObjectKey).
			Msg("添加文件MD5到Redis Set失败，文件已上传到MinIO")
		// 即使Redis添加失败，也继续流程，因为核心文件已上传。后续文本MD5去重是另一道防线。
	}

	// 4. 构建消息并发送到RabbitMQ
	message := storage2.ResumeUploadMessage{
		SubmissionUUID:      submissionUUID,
		OriginalFilePathOSS: originalObjectKey,
		OriginalFilename:    filename,
		TargetJobID:         targetJobID,
		SourceChannel:       sourceChannel,
		SubmissionTimestamp: time.Now(),

		// 兼容性字段 (保留以向后兼容旧版客户端)
		OriginalFileObjectKey: originalObjectKey,
	}

	// 发布消息到上传交换机
	err = h.storage.RabbitMQ.PublishJSON(
		ctx,
		h.cfg.RabbitMQ.ResumeEventsExchange,
		h.cfg.RabbitMQ.UploadedRoutingKey,
		message,
		true, // 持久化
	)
	if err != nil {
		return nil, fmt.Errorf("发布消息到RabbitMQ失败: %w", err)
	}

	// 5. 返回响应
	return &ResumeUploadResponse{
		SubmissionUUID: submissionUUID,
		Status:         "SUBMITTED_FOR_PROCESSING",
	}, nil
}

// StartResumeUploadConsumer 启动简历上传消费者
func (h *ResumeHandler) StartResumeUploadConsumer(ctx context.Context, batchSize int, batchTimeout time.Duration) error {
	// 添加日志打印当前配置中的交换机名称
	logger.Info().
		Str("exchange", h.cfg.RabbitMQ.ResumeEventsExchange).
		Str("routing_key", h.cfg.RabbitMQ.UploadedRoutingKey).
		Msg("初始化RabbitMQ配置")

	// 1. 确保交换机和队列存在
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ResumeEventsExchange, "direct", true); err != nil {
		return fmt.Errorf("确保交换机存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.RawResumeQueue, true); err != nil {
		return fmt.Errorf("确保队列存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.BindQueue(
		h.cfg.RabbitMQ.RawResumeQueue,
		h.cfg.RabbitMQ.ResumeEventsExchange,
		h.cfg.RabbitMQ.UploadedRoutingKey,
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err)
	}

	logger.Info().
		Str("queue", h.cfg.RabbitMQ.RawResumeQueue).
		Int("batch_size", batchSize).
		Dur("batch_timeout", batchTimeout).
		Msg("简历上传消费者就绪")

	// 启动消费者
	_, err := h.storage.RabbitMQ.StartConsumer(h.cfg.RabbitMQ.RawResumeQueue, batchSize, func(data []byte) bool {
		// 这里需要实现单个消息的处理逻辑
		var message storage2.ResumeUploadMessage
		if err := json.Unmarshal(data, &message); err != nil {
			logger.Error().Err(err).Msg("解析消息失败")
			return false
		}

		// 单条消息处理
		submissions := []models.ResumeSubmission{
			{
				SubmissionUUID:      message.SubmissionUUID,
				OriginalFilePathOSS: message.OriginalFilePathOSS,
				OriginalFilename:    message.OriginalFilename,
				TargetJobID:         utils.StringPtr(message.TargetJobID),
				SourceChannel:       message.SourceChannel,
				SubmissionTimestamp: message.SubmissionTimestamp,
				ProcessingStatus:    "PENDING_PARSING",
			},
		}
		if err := h.storage.MySQL.BatchInsertResumeSubmissions(ctx, submissions); err != nil {
			logger.Error().Err(err).Msg("插入简历提交记录失败")
			return false
		}

		if err := h.processResumeText(ctx, message); err != nil {
			logger.Error().
				Err(err).
				Str("submission_uuid", message.SubmissionUUID).
				Msg("处理简历文本失败")
			if err := h.storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "TEXT_EXTRACTION_FAILED"); err != nil {
				logger.Error().Err(err).Str("submission_uuid", message.SubmissionUUID).Msg("更新简历状态为TEXT_EXTRACTION_FAILED失败")
			}
			return false
		}

		return true
	})

	if err != nil {
		return fmt.Errorf("启动消费者失败: %w", err)
	}

	return nil
}

// processResumeText 处理简历文本提取
func (h *ResumeHandler) processResumeText(ctx context.Context, message storage2.ResumeUploadMessage) error {
	// 检查处理器是否初始化
	if h.processorModule == nil || h.processorModule.PDFExtractor == nil {
		return fmt.Errorf("PDF提取器组件未初始化")
	}

	// 1. 从MinIO下载原始文件内容
	fileContentBytes, err := h.storage.MinIO.GetResumeFile(ctx, message.OriginalFilePathOSS)
	if err != nil {
		return fmt.Errorf("从MinIO获取简历文件失败: %w", err)
	}

	// 2. 使用聚合类中的PDF提取器提取文本
	text, _, err := h.processorModule.PDFExtractor.ExtractTextFromReader(
		ctx,
		bytes.NewReader(fileContentBytes),
		message.OriginalFilePathOSS,
		nil,
	)
	if err != nil {
		return fmt.Errorf("提取简历文本失败: %w", err)
	}

	// 2.5. 计算提取文本的MD5并进行去重检查
	textMD5Hex := utils.CalculateMD5([]byte(text))

	// 将文本MD5存入 resume_submissions 表的 raw_text_md5 字段，无论是否重复
	// 这样做可以方便后续分析，知道某个提交是哪个文本内容的副本
	if err := h.storage.MySQL.UpdateResumeRawTextMD5(ctx, message.SubmissionUUID, textMD5Hex); err != nil {
		// 即使这里失败，也尝试继续去重检查，但记录错误
		logger.Warn().
			Err(err).
			Str("submission", message.SubmissionUUID).
			Str("textMD5", textMD5Hex).
			Msg("更新 raw_text_md5 到数据库失败")
	}

	textExists, err := h.storage.Redis.CheckParsedTextMD5Exists(ctx, textMD5Hex)
	if err != nil {
		logger.Warn().
			Err(err).
			Str("submission", message.SubmissionUUID).
			Str("textMD5", textMD5Hex).
			Msg("查询Redis文本MD5 Set失败，将继续处理，但文本去重可能失效")
		// 如果Redis查询失败，选择继续处理而不是阻塞流程，但文本去重将不起作用
		// 另一种策略是返回错误，让消息重试或进死信队列，但这可能导致有效简历无法处理
	} else if textExists {
		logger.Info().
			Str("textMD5", textMD5Hex).
			Str("submission", message.SubmissionUUID).
			Msg("检测到重复的文本MD5，跳过后续LLM处理")
		if err := h.storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "CONTENT_DUPLICATE_SKIPPED"); err != nil {
			logger.Warn().
				Err(err).
				Str("submission", message.SubmissionUUID).
				Msg("更新状态为CONTENT_DUPLICATE_SKIPPED失败")
		}
		return nil // 成功处理（通过跳过重复内容）
	}

	// 3. 将文本存储到MinIO
	textObjectKey, err := h.storage.MinIO.UploadParsedText(ctx, message.SubmissionUUID, text)
	if err != nil {
		return fmt.Errorf("上传解析文本到MinIO失败: %w", err)
	}

	// 4. 更新数据库状态 (使用GORM) // 这个状态会被后续的 QUEUED_FOR_LLM_PARSING 覆盖
	// 原始逻辑：err = h.updateResumeTextInfoGORM(message.SubmissionUUID, textObjectKey)
	// 我们不再单独调用 updateResumeTextInfoGORM，因为它的状态会被覆盖。
	// raw_text_md5 已经在前面更新了。parsed_text_path_oss 会在下面消息发布前更新到DB。

	// 5. 发送消息到LLM解析队列
	processingMessage := storage2.ResumeProcessingMessage{
		SubmissionUUID:    message.SubmissionUUID,
		ParsedTextPathOSS: textObjectKey,
		TargetJobID:       message.TargetJobID,

		// 兼容性字段 (保留以向后兼容旧版客户端)
		ParsedTextObjectKey: textObjectKey,
	}

	err = h.storage.RabbitMQ.PublishJSON(
		ctx,
		h.cfg.RabbitMQ.ProcessingEventsExchange,
		h.cfg.RabbitMQ.ParsedRoutingKey,
		processingMessage,
		true,
	)
	if err != nil {
		return fmt.Errorf("发布消息到LLM解析队列失败: %w", err)
	}

	// 在消息成功发布到MQ后 (或至少在同一事务中保证)，将新的文本MD5添加到Redis Set
	// 并且更新数据库中的 parsed_text_path_oss 和最终状态
	if !textExists { // 只有当文本MD5是新的，并且我们真的处理了它，才添加到Set
		if err := h.storage.Redis.AddParsedTextMD5(ctx, textMD5Hex); err != nil {
			logger.Warn().
				Err(err).
				Str("submission", message.SubmissionUUID).
				Str("textMD5", textMD5Hex).
				Msg("添加文本MD5到Redis Set失败，文本已发送处理")
			// 即使添加Redis Set失败，也已进入后续流程，记录日志。
		}
	}

	// 6. 更新数据库状态为已入队等待LLM解析，并记录parsed_text_path_oss
	updates := map[string]interface{}{
		"parsed_text_path_oss": textObjectKey,
		"processing_status":    "QUEUED_FOR_LLM",
		// raw_text_md5 已经在前面步骤中更新过了
	}
	return h.storage.MySQL.UpdateResumeSubmissionFields(h.storage.MySQL.DB().WithContext(ctx), message.SubmissionUUID, updates)
}

// ParallelProcessResumeTask 处理简历分块和岗位匹配评估 (使用聚合类)
func (h *ResumeHandler) ParallelProcessResumeTask(ctx context.Context, message storage2.ResumeProcessingMessage) error {
	// 1. 获取解析后的文本
	var text string
	var err error

	if message.ParsedText != "" {
		text = message.ParsedText
	} else if message.ParsedTextPathOSS != "" {
		// 优先使用主字段名
		text, err = h.storage.MinIO.GetParsedText(ctx, message.ParsedTextPathOSS)
		if err != nil {
			return fmt.Errorf("从MinIO获取解析文本失败: %w", err)
		}
	} else if message.ParsedTextObjectKey != "" {
		// 兼容旧字段
		text, err = h.storage.MinIO.GetParsedText(ctx, message.ParsedTextObjectKey)
		if err != nil {
			return fmt.Errorf("从MinIO获取解析文本失败: %w", err)
		}
	} else {
		return fmt.Errorf("消息中既没有文本内容也没有文本对象键")
	}

	// 获取岗位描述（如果有）
	var jobDescription string
	if message.TargetJobID != "" {
		// 临时方案：从数据库获取
		var job models.Job
		if err := h.storage.MySQL.DB().Where("job_id = ?", message.TargetJobID).First(&job).Error; err != nil {
			logger.Warn().
				Err(err).
				Msg("获取岗位信息失败，将继续处理但不进行岗位匹配")
		} else {
			jobDescription = job.JobDescriptionText
		}
	}

	// 检查处理器模块是否可用
	if h.processorModule == nil {
		return fmt.Errorf("处理器模块未初始化")
	}

	// 使用处理器组件分别执行操作，而不是使用Process方法
	var sections []*types.ResumeSection
	var basicInfo map[string]string
	var evaluation *types.JobMatchEvaluation
	var wg sync.WaitGroup
	var errChan = make(chan error, 2)
	var doneChan = make(chan struct{})

	// 只有当分块器可用时才进行分块操作
	if h.processorModule.ResumeChunker != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			sections, basicInfo, err = h.processorModule.ResumeChunker.ChunkResume(ctx, text)
			if err != nil {
				errChan <- fmt.Errorf("简历分块失败: %w", err)
			}
		}()
	}

	// 只有当评估器和岗位描述都可用时才进行评估
	if h.processorModule.MatchEvaluator != nil && jobDescription != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			evaluation, err = h.processorModule.MatchEvaluator.EvaluateMatch(ctx, jobDescription, text)
			if err != nil {
				errChan <- fmt.Errorf("岗位匹配评估失败: %w", err)
			}
		}()
	}

	// 等待所有goroutine完成
	go func() {
		wg.Wait()
		close(doneChan)
	}()

	// 等待完成或错误
	select {
	case err := <-errChan:
		if errDb := h.storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "LLM_PROCESSING_FAILED"); errDb != nil {
			logger.Error().Err(errDb).Str("submission_uuid", message.SubmissionUUID).Msg("更新简历状态为LLM_PROCESSING_FAILED失败")
		}
		return err
	case <-doneChan:
		// 所有goroutine正常完成
		break
	case <-ctx.Done():
		if errDb := h.storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "LLM_PROCESSING_FAILED"); errDb != nil {
			logger.Error().Err(errDb).Str("submission_uuid", message.SubmissionUUID).Msg("更新简历状态为LLM_PROCESSING_FAILED失败")
		}
		return ctx.Err()
	}

	// 如果分块和嵌入器都可用，并且分块结果不为空，可以进行向量嵌入
	// 注意：此处仅计算向量，但由于移除了vectorStore，我们不再存储向量
	if h.processorModule.ResumeEmbedder != nil && sections != nil && len(sections) > 0 {
		chunkEmbeddings, err := h.processorModule.ResumeEmbedder.Embed(ctx, sections, basicInfo)
		if err != nil {
			logger.Warn().
				Err(err).
				Str("submission_uuid", message.SubmissionUUID).
				Msg("向量嵌入失败，跳过向量存储")
			// 继续处理，不返回错误
		} else if chunkEmbeddings != nil && len(chunkEmbeddings) == len(sections) { // 确保嵌入向量数量与分块数量一致
			// 检查Qdrant存储是否已配置且可用
			if h.storage.Qdrant != nil {
				// 将 []*types.ResumeSection 转换为 []types.ResumeChunk
				qdrantChunks := make([]types.ResumeChunk, len(sections))

				// 从 []*types.ResumeChunkVector 提取 [][]float64
				floatEmbeddings := make([][]float64, len(chunkEmbeddings))
				for i, emb := range chunkEmbeddings {
					if emb != nil {
						floatEmbeddings[i] = emb.Vector
					} else {
						// 如果某个向量为空，也需要处理，例如填充一个空切片或记录错误
						floatEmbeddings[i] = []float64{}
						logger.Warn().
							Str("submission_uuid", message.SubmissionUUID).
							Int("chunk_index", i).
							Msg("发现空的嵌入向量，将使用空向量代替")
					}
				}

				// 尝试从 basicInfo 中获取全局元数据
				var globalExpYears int
				// 假设 basicInfo 可能包含如 "total_experience_years" 或 "experience_years" 的键
				if expStr, ok := basicInfo["experience_years"]; ok {
					parsedExp, parseErr := strconv.Atoi(expStr)
					if parseErr == nil {
						globalExpYears = parsedExp
					} else {
						logger.Warn().Str("submission_uuid", message.SubmissionUUID).Str("key", "experience_years").Str("value", expStr).Err(parseErr).Msg("无法将 basicInfo 中的 experience_years 解析为整数")
					}
				} else if expStr, ok := basicInfo["total_experience_years"]; ok { // 尝试另一个可能的键名
					parsedExp, parseErr := strconv.Atoi(expStr)
					if parseErr == nil {
						globalExpYears = parsedExp
					} else {
						logger.Warn().Str("submission_uuid", message.SubmissionUUID).Str("key", "total_experience_years").Str("value", expStr).Err(parseErr).Msg("无法将 basicInfo 中的 total_experience_years 解析为整数")
					}
				}

				// 提取基本信息用于填充 UniqueIdentifiers
				candidateName := basicInfo["name"]
				candidatePhone := basicInfo["phone"]
				candidateEmail := basicInfo["email"]

				// 假设 basicInfo 可能包含如 "highest_education_level" 或 "education_level" 的键
				globalEduLevel := basicInfo["education_level"]
				if globalEduLevel == "" {
					globalEduLevel = basicInfo["highest_education_level"] // 尝试另一个可能的键名
				}

				for i, section := range sections {
					var sectionType string
					if section != nil && section.Type != "" { // section.Type 假设为 string 或可转换为 string 的类型
						sectionType = string(section.Type)
					}

					var sectionContent string
					if section != nil && section.Content != "" {
						sectionContent = section.Content
					}

					// 为Qdrant准备的Chunk元数据
					chunkMetadata := types.ChunkMetadata{
						ExperienceYears: globalExpYears,
						EducationLevel:  globalEduLevel,
					}
					// 如果 types.ResumeSection 结构体包含更详细的元数据（例如 section.IdentifiedSkills, section.Score），
					// 则应在此处使用这些字段来填充 chunkMetadata 和 ImportanceScore。
					// 例如:
					// if section.Score > 0 { qdrantChunks[i].ImportanceScore = section.Score }
					// if len(section.Skills) > 0 { chunkMetadata.Skills = section.Skills }

					// 根据ChunkType设置ImportanceScore
					var importanceScore float32
					switch types.SectionType(sectionType) { // 确保 sectionType 转换为 types.SectionType
					case types.SectionWorkExperience, types.SectionProjects, types.SectionSkills:
						importanceScore = 1.5
					case types.SectionEducation, types.SectionBasicInfo:
						importanceScore = 1.2
					default:
						importanceScore = 1.0
					}

					qdrantChunks[i] = types.ResumeChunk{
						ChunkID:         i + 1, // 使用基于1的索引作为Qdrant的ChunkID
						ChunkType:       sectionType,
						Content:         sectionContent,
						ImportanceScore: importanceScore, // 使用动态计算的ImportanceScore
						UniqueIdentifiers: types.Identity{ // 填充UniqueIdentifiers
							Name:  candidateName,
							Phone: candidatePhone,
							Email: candidateEmail,
						},
						Metadata: chunkMetadata,
					}
				}

				// 存储向量到Qdrant
				pointIDs, storeErr := h.storage.Qdrant.StoreResumeVectors(ctx, message.SubmissionUUID, qdrantChunks, floatEmbeddings)
				if storeErr != nil {
					logger.Warn().
						Err(storeErr).
						Str("submission_uuid", message.SubmissionUUID).
						Msg("存储简历向量到Qdrant失败")
					// 不因向量存储失败而使整个流程失败，仅记录警告
				} else {
					logger.Info().
						Str("submission_uuid", message.SubmissionUUID).
						Int("num_vectors_stored", len(pointIDs)).
						Msg("成功存储简历向量到Qdrant")
				}
			} else {
				logger.Warn().
					Str("submission_uuid", message.SubmissionUUID).
					Msg("Qdrant存储未配置或不可用，跳过向量存储")
			}
		} else if chunkEmbeddings != nil && len(chunkEmbeddings) != len(sections) {
			// 如果嵌入向量的数量与分块数量不匹配，记录警告
			logger.Warn().
				Str("submission_uuid", message.SubmissionUUID).
				Int("num_sections", len(sections)).
				Int("num_embeddings", len(chunkEmbeddings)).
				Msg("向量嵌入数量与分块数量不匹配，跳过向量存储")
		}
	}

	// 合并写库操作，减少IO次数
	err = h.storage.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 写入分块结果 (如果有)
		if sections != nil {
			if err := h.storage.MySQL.SaveResumeChunks(tx, message.SubmissionUUID, sections); err != nil {
				return fmt.Errorf("保存简历块失败: %w", err)
			}
		}

		// 2. 写入基本信息 (如果有)
		// 注意：saveResumeBasicInfoGORM 内部也会更新 resume_submissions 表，
		// 如果它更新的字段与下面的 Updates 冲突或覆盖，需要调整。
		// 目前 saveResumeBasicInfoGORM 更新 llm_parsed_basic_info 和 llm_resume_identifier
		if basicInfo != nil {
			if err := h.storage.MySQL.SaveResumeBasicInfo(tx, message.SubmissionUUID, basicInfo); err != nil {
				return fmt.Errorf("保存简历基本信息失败: %w", err)
			}
		}

		// 3. 写入岗位匹配评估结果 (如果有)
		if evaluation != nil {
			match := models.JobSubmissionMatch{
				SubmissionUUID:         message.SubmissionUUID,
				JobID:                  message.TargetJobID,
				LLMMatchScore:          &evaluation.MatchScore,
				LLMMatchHighlightsJSON: utils.ConvertArrayToJSON(evaluation.MatchHighlights),
				LLMPotentialGapsJSON:   utils.ConvertArrayToJSON(evaluation.PotentialGaps),
				LLMResumeSummaryForJD:  evaluation.ResumeSummaryForJD,
				EvaluationStatus:       "COMPLETED",
				EvaluatedAt:            utils.TimePtr(time.Unix(evaluation.EvaluatedAt, 0)),
			}

			if err := h.storage.MySQL.CreateJobSubmissionMatch(tx, &match); err != nil {
				return fmt.Errorf("保存岗位匹配评估结果失败: %w", err)
			}
		}

		// 4. 更新状态为处理完成
		// 从配置中获取当前激活的解析器版本
		currentParserVersion := h.cfg.ActiveParserVersion
		if len(currentParserVersion) > 50 { // 遵守数据库字段长度限制
			currentParserVersion = currentParserVersion[:50]
		}

		updateData := map[string]interface{}{
			"processing_status": "PROCESSING_COMPLETED",
			"parser_version":    currentParserVersion, // 设置 parser_version
			// candidate_id 等其他字段的更新需要在此处或 saveResumeBasicInfoGORM 中处理
		}

		if err := h.storage.MySQL.UpdateResumeSubmissionFields(tx, message.SubmissionUUID, updateData); err != nil {
			// 如果 saveResumeBasicInfoGORM 已经更新了部分字段，这里的 Updates 可能会覆盖或冲突
			// 需要确保更新逻辑的正确性。
			// 例如，如果 basicInfo 更新也包含了 status，那么这里的 status 更新需要协调。
			// 鉴于 saveResumeBasicInfoGORM 只更新特定字段，这里直接 Updates 通常问题不大，
			// 但如果是更复杂的场景，可能需要合并更新的 map 或分步更新。
			return fmt.Errorf("更新处理状态和解析器版本失败: %w", err)
		}

		return nil
	})

	if err != nil {
		if errDb := h.storage.MySQL.UpdateResumeProcessingStatus(ctx, message.SubmissionUUID, "LLM_PROCESSING_FAILED"); errDb != nil {
			logger.Error().Err(errDb).Str("submission_uuid", message.SubmissionUUID).Msg("更新简历状态为LLM_PROCESSING_FAILED失败")
		}
		return fmt.Errorf("事务执行失败: %w", err)
	}

	return nil
}

// StartLLMParsingConsumer 启动LLM解析消费者
func (h *ResumeHandler) StartLLMParsingConsumer(ctx context.Context, prefetchCount int) error {
	// 1. 确保交换机和队列存在
	if err := h.storage.RabbitMQ.EnsureExchange(h.cfg.RabbitMQ.ProcessingEventsExchange, "direct", true); err != nil {
		return fmt.Errorf("确保交换机存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.EnsureQueue(h.cfg.RabbitMQ.LLMParsingQueue, true); err != nil {
		return fmt.Errorf("确保队列存在失败: %w", err)
	}

	if err := h.storage.RabbitMQ.BindQueue(
		h.cfg.RabbitMQ.LLMParsingQueue,
		h.cfg.RabbitMQ.ProcessingEventsExchange,
		h.cfg.RabbitMQ.ParsedRoutingKey,
	); err != nil {
		return fmt.Errorf("绑定队列失败: %w", err)
	}

	logger.Info().
		Str("queue", h.cfg.RabbitMQ.LLMParsingQueue).
		Int("prefetch_count", prefetchCount).
		Msg("LLM解析消费者就绪")

	// 2. 启动消费者
	_, err := h.storage.RabbitMQ.StartConsumer(h.cfg.RabbitMQ.LLMParsingQueue, prefetchCount, func(data []byte) bool {
		var message storage2.ResumeProcessingMessage
		if err := json.Unmarshal(data, &message); err != nil {
			logger.Error().
				Err(err).
				Msg("解析消息失败")
			return false
		}

		// 使用协程池处理任务
		if err := h.ParallelProcessResumeTask(ctx, message); err != nil {
			logger.Error().
				Err(err).
				Str("submissionUUID", message.SubmissionUUID).
				Msg("处理简历任务失败")
			return false
		}

		return true
	})

	if err != nil {
		return fmt.Errorf("启动消费者失败: %w", err)
	}

	return nil
}

// StartMD5CleanupTask 启动MD5记录清理任务
// 此方法可选调用，用于定期检查和重置MD5记录的过期时间
func (h *ResumeHandler) StartMD5CleanupTask(ctx context.Context) {
	// 定义清理间隔，默认每周执行一次
	cleanupInterval := 7 * 24 * time.Hour

	logger.Info().
		Dur("interval", cleanupInterval).
		Msg("启动MD5记录清理任务")

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	// 立即执行一次清理
	h.cleanupMD5Records(ctx)

	for {
		select {
		case <-ticker.C:
			h.cleanupMD5Records(ctx)
		case <-ctx.Done():
			logger.Info().Msg("MD5记录清理任务退出")
			return
		}
	}
}

// cleanupMD5Records 清理MD5记录
// 此方法会检查MD5集合是否有过期时间，如果没有则设置
func (h *ResumeHandler) cleanupMD5Records(ctx context.Context) {
	logger.Info().Msg("执行MD5记录清理任务...")

	// 使用新的内部常量来检查和设置文件MD5集合的过期时间
	ttlFile, errFile := h.storage.Redis.Client.TTL(ctx, constants.RawFileMD5SetKey).Result()
	if errFile != nil {
		logger.Error().Err(errFile).Str("setKey", constants.RawFileMD5SetKey).Msg("获取文件MD5集合过期时间失败")
	} else if ttlFile < 0 {
		expiry := h.storage.Redis.GetMD5ExpireDuration()
		if err := h.storage.Redis.Client.Expire(ctx, constants.RawFileMD5SetKey, expiry).Err(); err != nil {
			logger.Error().Err(err).Str("setKey", constants.RawFileMD5SetKey).Msg("设置文件MD5集合过期时间失败")
		} else {
			logger.Info().Str("setKey", constants.RawFileMD5SetKey).Dur("expiry", expiry).Msg("成功设置文件MD5集合过期时间")
		}
	}

	// 使用新的内部常量来检查和设置文本MD5集合的过期时间
	ttlText, errText := h.storage.Redis.Client.TTL(ctx, constants.ParsedTextMD5SetKey).Result()
	if errText != nil {
		logger.Error().Err(errText).Str("setKey", constants.ParsedTextMD5SetKey).Msg("获取文本MD5集合过期时间失败")
	} else if ttlText < 0 {
		expiry := h.storage.Redis.GetMD5ExpireDuration()
		if err := h.storage.Redis.Client.Expire(ctx, constants.ParsedTextMD5SetKey, expiry).Err(); err != nil {
			logger.Error().Err(err).Str("setKey", constants.ParsedTextMD5SetKey).Msg("设置文本MD5集合过期时间失败")
		} else {
			logger.Info().Str("setKey", constants.ParsedTextMD5SetKey).Dur("expiry", expiry).Msg("成功设置文本MD5集合过期时间")
		}
	}

	logger.Info().Msg("MD5记录清理任务完成")
}
