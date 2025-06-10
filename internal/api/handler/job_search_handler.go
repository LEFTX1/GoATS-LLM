package handler

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

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
	// 1. 获取请求参数
	jobID := c.Param("job_id")
	if jobID == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "job_id 不能为空"})
		return
	}

	// 从认证中间件获取 hr_id
	hrIDValue, exists := c.Get("hr_id")
	if !exists {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取 HR ID 失败"})
		return
	}
	hrID := hrIDValue.(string)

	// 获取分页参数
	displayLimitStr := c.Query("display_limit")
	displayLimit, err := strconv.Atoi(displayLimitStr)
	if err != nil || displayLimit <= 0 {
		displayLimit = 20 // 默认值
	}

	cursorStr := c.Query("cursor")
	cursor, err := strconv.Atoi(cursorStr)
	if err != nil || cursor < 0 {
		cursor = 0 // 默认值
	}

	h.logger.Printf("开始处理 JobID: %s 的简历搜索请求, HR: %s, Limit: %d, Cursor: %d", jobID, hrID, displayLimit, cursor)

	// 2. 生成唯一的、可复现的搜索会话ID和锁ID
	searchID := fmt.Sprintf("search_session:%s", jobID)
	lockKey := fmt.Sprintf("search_lock:%s", jobID)

	// 3. 检查缓存
	// 尝试从Redis缓存中获取"黄金结果集"的分页数据
	cachedUUIDs, totalCount, err := h.storage.Redis.GetCachedSearchResults(ctx, searchID, int64(cursor), int64(displayLimit))
	if err == nil && len(cachedUUIDs) > 0 {
		h.logger.Printf("缓存命中 for searchID: %s. 返回 %d 个简历UUIDs。", searchID, len(cachedUUIDs))
		// 缓存命中，直接从数据库查询这些UUID的详细信息并返回
		detailedResumes, dbErr := h.fetchResumeDetailsByUUIDs(ctx, cachedUUIDs)
		if dbErr != nil {
			h.logger.Printf("从数据库查询 Submission 详情失败 (UUIDs: %v): %v", cachedUUIDs, dbErr)
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取简历详情失败"})
			return
		}
		c.JSON(consts.StatusOK, map[string]interface{}{
			"message":     "搜索成功 (来自缓存)",
			"data":        detailedResumes,
			"job_id":      jobID,
			"total_count": totalCount,
			"next_cursor": cursor + len(cachedUUIDs),
		})
		return
	}

	// 4. 缓存未命中且为首次请求，执行完整搜索流程
	if cursor == 0 {
		h.logger.Printf("缓存未命中 for searchID: %s, 执行完整搜索流程...", searchID)

		// 4.1 尝试获取分布式锁，避免并发执行相同搜索
		lockValue, err := h.storage.Redis.AcquireLock(ctx, lockKey, 5*time.Minute)
		if err != nil {
			h.logger.Printf("获取分布式锁失败 for JobID %s: %v，继续执行可能导致重复处理", jobID, err)
		} else if lockValue == "" {
			// 未能获取锁，表示已有其他进程正在处理相同搜索
			h.logger.Printf("搜索已在处理中 for JobID: %s，返回等待消息", jobID)
			c.JSON(consts.StatusAccepted, map[string]interface{}{
				"message":     "您的搜索请求正在处理中，请稍后重试",
				"status":      "processing",
				"job_id":      jobID,
				"retry_after": 2, // 建议客户端2秒后重试
			})
			return
		} else {
			// 成功获取锁，在函数结束时释放
			defer func() {
				released, err := h.storage.Redis.ReleaseLock(ctx, lockKey, lockValue)
				if err != nil || !released {
					h.logger.Printf("释放锁失败 for JobID: %s: %v, released: %v", jobID, err, released)
				} else {
					h.logger.Printf("成功释放锁 for JobID: %s", jobID)
				}
			}()
		}

		// 执行完整的召回、重排、聚合流程
		finalRankedSubmissions, err := h.executeFullSearchPipeline(ctx, jobID, hrID)
		if err != nil {
			h.logger.Printf("完整搜索流程失败 for JobID %s: %v", jobID, err)
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "执行搜索失败"})
			return
		}

		if len(finalRankedSubmissions) == 0 {
			h.logger.Printf("完整搜索流程 for JobID %s 未找到任何结果", jobID)
			c.JSON(consts.StatusOK, map[string]interface{}{"message": "没有找到匹配的简历", "data": []interface{}{}})
			return
		}

		// 将"黄金结果集"写入缓存
		err = h.storage.Redis.CacheSearchResults(ctx, searchID, finalRankedSubmissions, 30*time.Minute)
		if err != nil {
			// 只记录日志，不阻塞主流程
			h.logger.Printf("缓存黄金结果集失败 for searchID %s: %v", searchID, err)
		}

		// 从刚生成的"黄金结果集"中取第一页数据
		pageUUIDs := make([]string, 0, displayLimit)
		end := cursor + displayLimit
		if end > len(finalRankedSubmissions) {
			end = len(finalRankedSubmissions)
		}

		for i := cursor; i < end; i++ {
			pageUUIDs = append(pageUUIDs, finalRankedSubmissions[i].SubmissionUUID)
		}

		// 查询详情并返回
		detailedResumes, dbErr := h.fetchResumeDetailsByUUIDs(ctx, pageUUIDs)
		if dbErr != nil {
			h.logger.Printf("从数据库查询 Submission 详情失败 (UUIDs: %v): %v", pageUUIDs, dbErr)
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取简历详情失败"})
			return
		}
		c.JSON(consts.StatusOK, map[string]interface{}{
			"message":     "搜索成功",
			"data":        detailedResumes,
			"job_id":      jobID,
			"total_count": len(finalRankedSubmissions),
			"next_cursor": cursor + len(detailedResumes),
		})
		return
	}

	// 5. 非首次请求但缓存失效或已读完，返回空
	h.logger.Printf("非首次请求但缓存未命中 for searchID: %s, Cursor: %d. 可能已浏览完所有结果。", searchID, cursor)
	c.JSON(consts.StatusOK, map[string]interface{}{
		"message":     "已查看所有匹配的简历",
		"data":        []interface{}{},
		"job_id":      jobID,
		"total_count": totalCount, // 从缓存查询中获取的总数
		"next_cursor": cursor,
	})
}

// fetchResumeDetailsByUUIDs 是一个辅助函数，用于根据UUID列表从数据库批量获取简历详情
func (h *JobSearchHandler) fetchResumeDetailsByUUIDs(ctx context.Context, uuids []string) ([]map[string]interface{}, error) {
	if len(uuids) == 0 {
		return []map[string]interface{}{}, nil
	}

	var submissions []models.ResumeSubmission
	// 使用 Preload 加载关联的 Candidate 信息
	err := h.storage.MySQL.DB().WithContext(ctx).
		Preload("Candidate").
		Where("submission_uuid IN ?", uuids).
		Find(&submissions).Error
	if err != nil {
		return nil, err
	}

	// 为了保持返回顺序与传入的uuids一致，创建一个map
	submissionMap := make(map[string]models.ResumeSubmission, len(submissions))
	for _, sub := range submissions {
		submissionMap[sub.SubmissionUUID] = sub
	}

	var detailedResumes []map[string]interface{}
	for _, uuid := range uuids {
		if sub, ok := submissionMap[uuid]; ok {
			candidateName := ""
			candidateEmail := ""
			candidatePhone := ""
			if sub.Candidate != nil {
				candidateName = sub.Candidate.PrimaryName
				candidateEmail = sub.Candidate.PrimaryEmail
				candidatePhone = sub.Candidate.PrimaryPhone
			}

			detailedResumes = append(detailedResumes, map[string]interface{}{
				"submission_uuid":   sub.SubmissionUUID,
				"file_name":         sub.OriginalFilename,
				"source_channel":    sub.SourceChannel,
				"upload_time":       sub.SubmissionTimestamp.Format("2006-01-02 15:04:05"),
				"candidate_name":    candidateName,
				"candidate_email":   candidateEmail,
				"candidate_phone":   candidatePhone,
				"processing_status": sub.ProcessingStatus,
				// 这里暂时不返回分数和qdrant payload，因为它们存储在缓存中，需要额外逻辑获取
			})
		}
	}

	return detailedResumes, nil
}

// getJobDescription 辅助函数，用于获取岗位描述文本。
// 首先尝试从 Redis 缓存获取，如果缓存未命中，则从 MySQL 数据库查询，并回填缓存。
func (h *JobSearchHandler) getJobDescription(ctx context.Context, jobID string) (string, error) {
	cacheKey := fmt.Sprintf("%s%s", constants.JobDescriptionTextCachePrefix, jobID)

	// 1. 尝试从 Redis 缓存获取 JD 文本
	cachedJD, err := h.storage.Redis.Get(ctx, cacheKey)
	if err == nil && cachedJD != "" {
		h.logger.Printf("从 Redis 缓存命中 JD 文本 for JobID: %s", jobID)
		return cachedJD, nil
	}
	if err != nil {
		// 如果是 redis.Nil 之外的错误，记录一下，但继续从数据库获取
		if !errors.Is(err, storage.ErrNotFound) {
			h.logger.Printf("从 Redis 获取 JD 文本缓存失败 for JobID: %s: %v", jobID, err)
		}
	}

	// 2. 缓存未命中，从 MySQL 直接获取
	h.logger.Printf("Redis 缓存未命中 JD 文本 for JobID: %s，从 MySQL 查询", jobID)
	var job models.Job
	err = h.storage.MySQL.DB().WithContext(ctx).Where("job_id = ?", jobID).First(&job).Error
	if err != nil {
		return "", err // 包括 gorm.ErrRecordNotFound
	}

	if job.JobDescriptionText == "" {
		return "", fmt.Errorf("job_id %s 对应的岗位描述为空", jobID)
	}

	// 3. 获取到 JD 后，将其存入 Redis 缓存
	// 使用一个合理的过期时间，例如 24 小时
	cacheDuration := 24 * time.Hour
	err = h.storage.Redis.Set(ctx, cacheKey, job.JobDescriptionText, cacheDuration)
	if err != nil {
		// 缓存失败不应阻塞主流程，但需要记录日志
		h.logger.Printf("将 JD 文本存入 Redis 失败 for JobID: %s: %v", jobID, err)
	} else {
		h.logger.Printf("成功将 JD 文本存入 Redis 缓存 for JobID: %s", jobID)
	}

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

func (h *JobSearchHandler) executeFullSearchPipeline(ctx context.Context, jobID string, hrID string) ([]types.RankedSubmission, error) {
	// 记录流程开始及相关参数
	startTime := time.Now()
	h.logger.Printf("开始执行完整搜索流程 for JobID: %s, HR: %s", jobID, hrID)

	// 1. 定义固定召回和过滤参数
	const internalRecallLimit = 300
	const scoreThreshold = 0.50
	const rerankTopN = 150 // 仅对向量搜索分数最高的前150名进行重排

	// 2. 获取JD文本和向量
	jdText, err := h.getJobDescription(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("获取JD失败: %w", err)
	}
	jdVector, err := h.jdProcessor.GetJobDescriptionVector(ctx, jobID, jdText)
	if err != nil {
		return nil, fmt.Errorf("获取JD向量失败: %w", err)
	}
	h.logger.Printf("成功获取JD向量 for JobID: %s", jobID)

	// 3. Qdrant向量召回
	initialResults, err := h.storage.Qdrant.SearchSimilarResumes(ctx, jdVector, internalRecallLimit, nil)
	if err != nil {
		return nil, fmt.Errorf("Qdrant搜索失败: %w", err)
	}
	h.logger.Printf("Qdrant召回 %d 个初步结果 for JobID: %s", len(initialResults), jobID)

	// 4. 海选过滤 (基于向量搜索分数)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	h.logger.Printf("应用score_threshold=%.2f后, 剩下 %d 个候选结果进入重排", scoreThreshold, len(candidatesForRerank))

	// 5. TODO: 已读过滤 (为第二阶段保留)
	// unreadCandidates, err := h.storage.Redis.FilterUnreadSubmissions(ctx, hrID, candidatesForRerank)
	// ...

	// 6. 调用Reranker服务进行精排
	// 注意：为控制成本和提高效率，仅将TopN结果发送给Reranker
	rerankCandidates := candidatesForRerank
	if len(rerankCandidates) > rerankTopN {
		rerankCandidates = rerankCandidates[:rerankTopN]
	}

	rerankScores, err := h.callReranker(ctx, jdText, rerankCandidates)
	if err != nil {
		// Reranker失败不应阻塞流程，可以回退到使用向量分数
		h.logger.Printf("警告: Reranker调用失败: %v。将回退到使用原始向量搜索分数。", err)
		// 在回退逻辑中，需要聚合原始向量分数
	} else {
		h.logger.Printf("Reranker精排完成，返回 %d 个分数", len(rerankScores))
	}

	// 7. 分数聚合 (Top-K 平均分, "惩罚短板"策略)
	finalSubmissionScores := h.aggregateScores(candidatesForRerank, rerankScores)
	h.logger.Printf("使用Top-K聚合策略后，得到 %d 份简历的最终综合得分", len(finalSubmissionScores))

	// 8. 排序并返回最终的"黄金结果集"
	var rankedSubmissions []types.RankedSubmission
	for uuid, score := range finalSubmissionScores {
		rankedSubmissions = append(rankedSubmissions, types.RankedSubmission{
			SubmissionUUID: uuid,
			Score:          score,
		})
	}

	sort.Slice(rankedSubmissions, func(i, j int) bool {
		return rankedSubmissions[i].Score > rankedSubmissions[j].Score
	})

	// 记录流程结束及耗时
	h.logger.Printf("完整搜索流程结束 for JobID: %s，耗时: %v，返回 %d 个结果",
		jobID, time.Since(startTime), len(rankedSubmissions))

	return rankedSubmissions, nil
}

// callReranker 是一个辅助函数，用于调用外部的Reranker服务
func (h *JobSearchHandler) callReranker(ctx context.Context, query string, documents []storage.SearchResult) (map[string]float32, error) {
	rerankerURL := h.cfg.Reranker.URL
	if !h.cfg.Reranker.Enabled || rerankerURL == "" {
		h.logger.Println("Reranker未启用或URL未配置，跳过重排序")
		return nil, nil // Not an error, just skipping
	}

	type RerankDocument struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	}

	type RerankRequest struct {
		Query     string           `json:"query"`
		Documents []RerankDocument `json:"documents"`
	}

	type RerankedDocument struct {
		ID          string  `json:"id"`
		RerankScore float32 `json:"rerank_score"`
	}

	var rerankDocs []RerankDocument
	for _, doc := range documents {
		if content, ok := doc.Payload["chunk_content_text"].(string); ok {
			rerankDocs = append(rerankDocs, RerankDocument{
				ID:   doc.ID,
				Text: content,
			})
		}
	}

	if len(rerankDocs) == 0 {
		return nil, nil // No documents to rerank
	}

	reqBody, err := json.Marshal(RerankRequest{Query: query, Documents: rerankDocs})
	if err != nil {
		return nil, fmt.Errorf("序列化rerank请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rerankerURL, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("创建rerank HTTP请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 使用更健壮的 HTTP client
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:    10,
			IdleConnTimeout: 30 * time.Second,
		},
		Timeout: 15 * time.Second, // 增加超时
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("执行rerank请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Reranker服务返回非200状态码: %d, 响应: %s", resp.StatusCode, string(bodyBytes))
	}

	var rerankedDocs []RerankedDocument
	if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil {
		return nil, fmt.Errorf("解码rerank响应失败: %w", err)
	}

	rerankScores := make(map[string]float32)
	for _, doc := range rerankedDocs {
		rerankScores[doc.ID] = doc.RerankScore
	}

	return rerankScores, nil
}

// aggregateScores 是一个辅助函数，用于聚合每个简历的分数
func (h *JobSearchHandler) aggregateScores(candidates []storage.SearchResult, rerankScores map[string]float32) map[string]float32 {
	submissionChunkScores := make(map[string][]float32)

	// 如果 Reranker 失败，rerankScores 会是 nil，此时我们使用原始向量分数
	useVectorScore := rerankScores == nil

	// 收集每个submission的所有 chunk 分数
	for _, res := range candidates {
		var score float32
		var ok bool

		if useVectorScore {
			score = res.Score
			ok = true
		} else {
			score, ok = rerankScores[res.ID]
		}

		if ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], score)
			}
		}
	}

	// 计算Top-K平均分 ("惩罚短板"策略)
	finalSubmissionScores := make(map[string]float32)
	const topK = 3
	for uuid, scores := range submissionChunkScores {
		// 降序排序
		sort.Slice(scores, func(i, j int) bool {
			return scores[i] > scores[j]
		})

		// 取前K个或所有（如果不足K个）
		effectiveK := topK
		if len(scores) < effectiveK {
			effectiveK = len(scores)
		}

		var sum float32
		for i := 0; i < effectiveK; i++ {
			sum += scores[i]
		}
		// 动态分母聚合：避免对短简历进行双重惩罚
		finalSubmissionScores[uuid] = sum / float32(effectiveK)
	}

	return finalSubmissionScores
}

// HandleCheckSearchStatus 处理查询搜索状态的请求
// GET /api/v1/jobs/:job_id/search/status
func (h *JobSearchHandler) HandleCheckSearchStatus(ctx context.Context, c *app.RequestContext) {
	// 获取请求参数
	jobID := c.Param("job_id")
	if jobID == "" {
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "job_id 不能为空"})
		return
	}

	// 从认证中间件获取 hr_id（可选，如果需要根据HR筛选结果）
	_, exists := c.Get("hr_id")
	if !exists {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取 HR ID 失败"})
		return
	}
	// 注：此处暂时不使用hrID，但保持身份验证检查
	// 未来可以扩展为根据HR筛选或记录特定HR的查询

	// 生成相关ID
	searchID := fmt.Sprintf("search_session:%s", jobID)
	lockKey := fmt.Sprintf("search_lock:%s", jobID)

	// 1. 检查是否有缓存结果存在
	cachedUUIDs, totalCount, err := h.storage.Redis.GetCachedSearchResults(ctx, searchID, 0, 1)
	if err == nil && len(cachedUUIDs) > 0 {
		// 缓存已存在，意味着搜索已完成
		c.JSON(consts.StatusOK, map[string]interface{}{
			"status":      "completed",
			"job_id":      jobID,
			"total_count": totalCount,
			"message":     "搜索已完成，可以获取结果",
		})
		return
	}

	// 2. 检查是否有锁存在（表示搜索正在进行中）
	lockExists, err := h.storage.Redis.Client.Exists(ctx, lockKey).Result()
	if err != nil {
		h.logger.Printf("检查搜索锁状态失败 for JobID %s: %v", jobID, err)
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "检查搜索状态失败"})
		return
	}

	if lockExists > 0 {
		// 锁存在，意味着搜索正在进行中

		// 尝试获取锁的剩余过期时间
		ttl, err := h.storage.Redis.Client.TTL(ctx, lockKey).Result()
		if err != nil {
			h.logger.Printf("获取锁TTL失败 for JobID %s: %v", jobID, err)
			// 不返回错误，继续处理
			ttl = 0
		}

		c.JSON(consts.StatusOK, map[string]interface{}{
			"status":      "processing",
			"job_id":      jobID,
			"message":     "您的搜索请求正在处理中，请稍后重试",
			"retry_after": 2, // 建议客户端2秒后重试
			"ttl_seconds": int(ttl.Seconds()),
		})
		return
	}

	// 3. 既没有缓存结果，也没有锁，意味着尚未开始搜索或者搜索失败
	c.JSON(consts.StatusOK, map[string]interface{}{
		"status":  "not_started",
		"job_id":  jobID,
		"message": "搜索尚未开始或之前的搜索已失败，请发起新的搜索请求",
	})
}
