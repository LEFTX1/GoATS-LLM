package handler

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"gorm.io/gorm"
)

// JobSearchHandler 负责处理与职位相关的搜索请求。
type JobSearchHandler struct {
	cfg         *config.Config
	storage     *storage.Storage
	jdProcessor *processor.JDProcessor
	logger      *log.Logger
}

// NewJobSearchHandler 创建一个新的 JobSearchHandler 实例。
func NewJobSearchHandler(cfg *config.Config, storage *storage.Storage, jdProcessor *processor.JDProcessor) *JobSearchHandler {
	return &JobSearchHandler{
		cfg:         cfg,
		storage:     storage,
		jdProcessor: jdProcessor,
		logger:      log.New(os.Stdout, "[JobSearchHandler] ", log.LstdFlags|log.Lshortfile),
	}
}

// HandleSearchResumesByJobID 处理根据 JobID 搜索简历的请求。
// GET /api/v1/jobs/:job_id/resumes/search
func (h *JobSearchHandler) HandleSearchResumesByJobID(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("job_id")
	if jobID == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "job_id 不能为空"})
		return
	}

	h.logger.Printf("开始处理 JobID: %s 的简历搜索请求", jobID)

	// 1. 获取岗位描述 (JD)
	jdText, err := h.getJobDescription(ctx, jobID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Printf("JobID: %s 未找到", jobID)
			c.JSON(consts.StatusNotFound, map[string]string{"error": fmt.Sprintf("未找到 JobID 为 %s 的岗位", jobID)})
		} else {
			h.logger.Printf("获取 JobID: %s 的岗位描述失败: %v", jobID, err)
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取岗位描述失败"})
		}
		return
	}
	h.logger.Printf("成功获取 JobID: %s 的 JD 文本，长度: %d", jobID, len(jdText))

	// 2. 获取 JD 向量
	jdVector, err := h.jdProcessor.GetJobDescriptionVector(ctx, jdText)
	if err != nil {
		h.logger.Printf("为 JobID: %s 生成 JD 向量失败: %v", jobID, err)
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "生成 JD 向量失败"})
		return
	}
	h.logger.Printf("成功为 JobID: %s 生成 JD 向量", jobID)

	// 3. 在 Qdrant 中搜索相似简历分块
	// 示例：搜索前5个最相似的，不过滤
	// TODO: 从请求参数中获取 limit 和 filter 条件
	limit := h.cfg.Qdrant.DefaultSearchLimit
	if limit <= 0 {
		limit = 10 // 默认值
	}

	// 组装 Qdrant 过滤条件 (基于 ResumeChunkVector.Metadata)
	// 当前只使用 job_id 进行最基础的演示性过滤（如果适用）
	// 实际场景中，这里会从请求参数解析更复杂的过滤条件
	qdrantFilter := map[string]interface{}{
		"must": []map[string]interface{}{
			//示例: 如果要在qdrant中根据 work_years > 5 过滤
			//{
			//	"key": "experience_years",
			//	"range": map[string]interface{}{
			//		"gte": 5,
			//	},
			//},
		},
	}
	// 如果 qdrantFilter 的 must 为空，则在 searchRequest 中不传递 filter 字段
	var searchRequestFilter map[string]interface{}
	if len(qdrantFilter["must"].([]map[string]interface{})) > 0 {
		searchRequestFilter = qdrantFilter
	}

	searchResults, err := h.storage.Qdrant.SearchSimilarResumes(ctx, jdVector, limit, searchRequestFilter)
	if err != nil {
		h.logger.Printf("在 Qdrant 中为 JobID: %s 搜索相似简历失败: %v", jobID, err)
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "搜索相似简历失败"})
		return
	}

	if len(searchResults) == 0 {
		h.logger.Printf("JobID: %s 没有找到匹配的简历", jobID)
		c.JSON(consts.StatusOK, map[string]interface{}{"message": "没有找到匹配的简历", "data": []interface{}{}})
		return
	}
	h.logger.Printf("为 JobID: %s 在 Qdrant 中找到 %d 个相似结果", jobID, len(searchResults))

	// 4. 聚合结果 (TODO: 实际场景中，需要根据 searchResults 中的 resume_id 或 submission_uuid 查询数据库获取更详细信息)
	// 目前简单返回 Qdrant 的原始 payload

	// 提取 submission_uuid 列表并去重
	submissionUUIDsMap := make(map[string]bool)
	for _, sr := range searchResults {
		if payloadResumeID, ok := sr.Payload["resume_id"].(string); ok && payloadResumeID != "" {
			submissionUUIDsMap[payloadResumeID] = true
		}
	}
	var uniqueSubmissionUUIDs []string
	for uuid := range submissionUUIDsMap {
		uniqueSubmissionUUIDs = append(uniqueSubmissionUUIDs, uuid)
	}

	h.logger.Printf("提取到 %d 个唯一的 Submission UUIDs: %v", len(uniqueSubmissionUUIDs), uniqueSubmissionUUIDs)

	// 5. 从数据库获取简历详细信息
	var detailedResumes []map[string]interface{}
	if len(uniqueSubmissionUUIDs) > 0 {
		var submissions []models.ResumeSubmission
		// 使用 Preload 加载关联的 Candidate 信息
		err = h.storage.MySQL.DB().WithContext(ctx).
			Preload("Candidate").
			Where("submission_uuid IN ?", uniqueSubmissionUUIDs).
			Order("submission_timestamp DESC"). // 可以根据需要排序
			Find(&submissions).Error

		if err != nil {
			h.logger.Printf("从数据库查询 Submission 详情失败 (UUIDs: %v): %v", uniqueSubmissionUUIDs, err)
			// 即使查询失败，也可能返回部分之前Qdrant的结果，或者直接报错
			// 这里选择继续，但标记错误
		}

		for _, sub := range submissions {
			// 查找对应的Qdrant原始得分（这里简化处理，实际可能需要更复杂的匹配逻辑）
			var bestScoreForSubmission float32 = 0
			var qdrantPayloadForSubmission map[string]interface{}

			for _, sr := range searchResults {
				if pid, ok := sr.Payload["resume_id"].(string); ok && pid == sub.SubmissionUUID {
					if sr.Score > bestScoreForSubmission {
						bestScoreForSubmission = sr.Score
						qdrantPayloadForSubmission = sr.Payload
					}
				}
			}

			candidateName := ""
			candidateEmail := ""
			candidatePhone := ""
			if sub.Candidate != nil {
				candidateName = sub.Candidate.PrimaryName
				candidateEmail = sub.Candidate.PrimaryEmail
				candidatePhone = sub.Candidate.PrimaryPhone
			}

			detailedResumes = append(detailedResumes, map[string]interface{}{
				"submission_uuid":        sub.SubmissionUUID,
				"file_name":              sub.OriginalFilename,
				"source_channel":         sub.SourceChannel,
				"upload_time":            sub.SubmissionTimestamp.Format("2006-01-02 15:04:05"),
				"candidate_name":         candidateName,
				"candidate_email":        candidateEmail,
				"candidate_phone":        candidatePhone,
				"parsed_basic_info":      sub.LLMParsedBasicInfo, // 已经是datatypes.JSON
				"processing_status":      sub.ProcessingStatus,
				"qdrant_score":           bestScoreForSubmission,
				"qdrant_payload_preview": qdrantPayloadForSubmission, // 包含如 chunk_type, content_preview 等
			})
		}
	}

	h.logger.Printf("成功处理 JobID: %s 的简历搜索请求，返回 %d 个聚合结果", jobID, len(detailedResumes))
	c.JSON(consts.StatusOK, map[string]interface{}{
		"message": "搜索成功",
		"data":    detailedResumes,
		"job_id":  jobID,
	})
}

// getJobDescription 辅助函数，用于获取岗位描述文本。
// 首先尝试从 Redis 缓存获取，如果缓存未命中，则从 MySQL 数据库查询。
func (h *JobSearchHandler) getJobDescription(ctx context.Context, jobID string) (string, error) {
	// 1. 尝试从 Redis 获取 JD 文本 (这里假设JD文本直接存在keywords那个key里, 或者有专门的JD文本缓存)
	// 根据现有 redis.go, 还没有直接缓存 JD 完整文本的函数，先假设 JobKeywordsCachePrefix 也可能存JD原文或结构化信息
	// 实际中，应该为JD原文设计专门的缓存key和逻辑

	// 我们先从 MySQL 直接获取
	var job models.Job
	err := h.storage.MySQL.DB().WithContext(ctx).Where("job_id = ?", jobID).First(&job).Error
	if err != nil {
		return "", err //包括 gorm.ErrRecordNotFound
	}

	if job.JobDescriptionText == "" {
		return "", fmt.Errorf("job_id %s 对应的岗位描述为空", jobID)
	}

	// TODO: 获取到 JD 后，可以考虑将其存入 Redis 缓存
	// 例如: h.storage.Redis.Set(ctx, fmt.Sprintf("jd_text_cache:%s", jobID), job.JobDescriptionText, 1*time.Hour)

	return job.JobDescriptionText, nil
}

// HandleGetJobDescription 处理获取特定岗位描述的请求 (辅助接口，可选)
// GET /api/v1/jobs/:job_id/description
func (h *JobSearchHandler) HandleGetJobDescription(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("job_id")
	if jobID == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "job_id 不能为空"})
		return
	}

	jdText, err := h.getJobDescription(ctx, jobID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			c.JSON(consts.StatusNotFound, map[string]string{"error": fmt.Sprintf("未找到 JobID 为 %s 的岗位", jobID)})
		} else {
			h.logger.Printf("获取 JobID: %s 的岗位描述失败: %v", jobID, err)
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取岗位描述失败"})
		}
		return
	}

	// 尝试从 Redis 获取 JD 向量信息（如果存在）
	vector, modelVersion, err := h.storage.Redis.GetJobVector(ctx, jobID)
	var vectorDim int
	if err == nil && len(vector) > 0 {
		vectorDim = len(vector)
	}

	// 尝试从 MySQL 获取 JobVector 信息
	dbJobVector, dbErr := h.storage.MySQL.GetJobVectorByID(h.storage.MySQL.DB().WithContext(ctx), jobID)
	var dbVectorDim int
	var dbModelVersion string
	if dbErr == nil && dbJobVector != nil && len(dbJobVector.VectorRepresentation) > 0 {
		// 假设 VectorRepresentation 是序列化的 float32 数组
		// 这里只是演示，实际反序列化可能更复杂
		var tempVec []float32
		if json.Unmarshal(dbJobVector.VectorRepresentation, &tempVec) == nil {
			dbVectorDim = len(tempVec)
		}
		dbModelVersion = dbJobVector.EmbeddingModelVersion
	}

	c.JSON(consts.StatusOK, map[string]interface{}{
		"job_id":              jobID,
		"description":         jdText,
		"description_len":     len(jdText),
		"cached_vector_dim":   vectorDim,
		"cached_vector_model": modelVersion,
		"db_vector_dim":       dbVectorDim,
		"db_vector_model":     dbModelVersion,
	})
}
