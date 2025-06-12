package handler

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
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

	// 2. 生成唯一的、可复现的搜索会话ID和锁ID，添加HR维度
	searchID := fmt.Sprintf(constants.KeySearchSession, jobID, hrID)
	lockKey := fmt.Sprintf(constants.KeySearchLock, jobID, hrID)

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
		err = h.storeResultsInCache(ctx, searchID, finalRankedSubmissions)
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
	cacheKey := fmt.Sprintf(constants.KeyJobDescriptionText, jobID)

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

// 用于高效获取TopK的最小堆
type MinHeap struct {
	values []float32
}

// Len 实现heap.Interface的Len()方法
func (h MinHeap) Len() int { return len(h.values) }

// Less 实现heap.Interface的Less()方法
func (h MinHeap) Less(i, j int) bool { return h.values[i] < h.values[j] }

// Swap 实现heap.Interface的Swap()方法
func (h MinHeap) Swap(i, j int) { h.values[i], h.values[j] = h.values[j], h.values[i] }

// Push 实现heap.Interface的Push()方法
func (h *MinHeap) Push(x interface{}) {
	h.values = append(h.values, x.(float32))
}

// Pop 实现heap.Interface的Pop()方法
func (h *MinHeap) Pop() interface{} {
	old := h.values
	n := len(old)
	x := old[n-1]
	h.values = old[0 : n-1]
	return x
}

// GetTopKSum 获取切片中最大的k个元素之和，不修改原切片
// 导出此函数，以便基准测试和扩展使用
func GetTopKSum(scores []float32, k int) (float32, int) {
	if len(scores) == 0 {
		return 0, 0
	}

	kk := k
	if len(scores) < k {
		kk = len(scores)
	}

	// 使用最小堆获取TopK
	h := &MinHeap{values: make([]float32, 0, kk)}

	// 首先填充k个元素
	for i := 0; i < len(scores) && i < kk; i++ {
		heap.Push(h, scores[i])
	}

	// 替换较小的元素
	for i := kk; i < len(scores); i++ {
		if scores[i] > h.values[0] { // 大于堆顶（最小值）
			heap.Pop(h)
			heap.Push(h, scores[i])
		}
	}

	// 计算总和
	var sum float32
	for _, v := range h.values {
		sum += v
	}

	return sum, kk
}

// ComputeStats 一次遍历计算均值、标准差和覆盖率
// 导出此函数，以便基准测试和扩展使用
func ComputeStats(scores []float32, coverTh float32) (mean float64, stdev float64, cover float32) {
	if len(scores) == 0 {
		return 0, 0, 0
	}

	var sum, sqSum float64
	var coverCount int

	// 一次遍历计算所有统计量
	for _, s := range scores {
		sum += float64(s)
		sqSum += float64(s) * float64(s)
		if s >= coverTh {
			coverCount++
		}
	}

	n := float64(len(scores))
	mean = sum / n
	stdev = sqSum/n - mean*mean // E[X²] - E[X]²
	if stdev < 0 {              // 避免数值精度问题导致负方差
		stdev = 0
	} else {
		stdev = math.Sqrt(stdev)
	}

	cover = float32(coverCount) / float32(len(scores))
	return mean, stdev, cover
}

// RerankDocument Reranker服务的输入文档结构
type RerankDocument struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// RerankRequest Reranker服务的请求结构
type RerankRequest struct {
	Query     string           `json:"query"`
	Documents []RerankDocument `json:"documents"`
}

// RerankedDocument Reranker服务的输出文档结构
type RerankedDocument struct {
	ID          string  `json:"id"`
	RerankScore float32 `json:"rerank_score"`
}

// executeFullSearchPipeline 执行完整的搜索流水线
func (h *JobSearchHandler) executeFullSearchPipeline(ctx context.Context, jobID string, hrID string) ([]types.RankedSubmission, error) {
	// 1. 获取JD文本和向量
	jdText, err := h.getJobDescription(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("获取JD失败: %w", err)
	}
	jdVector, err := h.jdProcessor.GetJobDescriptionVector(ctx, jobID, jdText)
	if err != nil {
		return nil, fmt.Errorf("获取JD向量失败: %w", err)
	}

	// 2. 向量数据库召回：场景 C.2 用更大的 K
	const recallLimit = 500
	initialResults, err := h.storage.Qdrant.SearchSimilarResumes(ctx, jdVector, recallLimit, nil)
	if err != nil {
		return nil, err
	}
	h.logger.Printf("向量召回获得 %d 个初步 chunk", len(initialResults))

	// 3. 候选人级聚合：每个 submission_uuid 选最高向量分数的 chunk
	bestPerResume := make(map[string]storage.SearchResult)
	for _, res := range initialResults {
		uuid, _ := res.Payload["submission_uuid"].(string)
		if uuid == "" {
			continue
		}
		if exist, ok := bestPerResume[uuid]; !ok || res.Score > exist.Score {
			bestPerResume[uuid] = res
		}
	}
	// 转 slice 并按向量分数降序，截取 Top 120
	var candidateChunks []storage.SearchResult
	for _, v := range bestPerResume {
		candidateChunks = append(candidateChunks, v)
	}
	sort.Slice(candidateChunks, func(i, j int) bool {
		return candidateChunks[i].Score > candidateChunks[j].Score
	})
	const candidateTopN = 120
	if len(candidateChunks) > candidateTopN {
		candidateChunks = candidateChunks[:candidateTopN]
	}
	h.logger.Printf("聚合后，%d 份候选简历（最佳 chunk）进入 Rerank", len(candidateChunks))

	// 4. 对候选 chunk 批量调用 Reranker
	rerankScores, err := h.batchReranker(ctx, jdText, candidateChunks)
	if err != nil {
		h.logger.Printf("Reranker 调用失败，退回向量排序：%v", err)
		rerankScores = nil // 即使reranker失败，后续也可以只用向量分数
	}

	// 5. 线性融合 (Min-Max 归一化 + 30% 向量分 + 70% Rerank 分)
	finalScores := make(map[string]float32, len(candidateChunks))

	if rerankScores != nil {
		// 5.1 先收集两组原始分数
		vecScores := make([]float32, 0, len(candidateChunks))
		rerScores := make([]float32, 0, len(candidateChunks))
		for _, ch := range candidateChunks {
			if _, ok := rerankScores[ch.ID]; ok {
				vecScores = append(vecScores, ch.Score)
				rerScores = append(rerScores, rerankScores[ch.ID])
			}
		}

		if len(vecScores) > 0 {
			// 5.2 找 min/max
			minMax := func(arr []float32) (min, max float32) {
				min, max = arr[0], arr[0]
				for _, v := range arr {
					if v < min {
						min = v
					}
					if v > max {
						max = v
					}
				}
				return
			}
			minV, maxV := minMax(vecScores)
			minR, maxR := minMax(rerScores)

			// 5.3 对每个候选人做归一化并融合
			const wVec, wRer = 0.3, 0.7
			for _, ch := range candidateChunks {
				if rerankScore, ok := rerankScores[ch.ID]; ok {
					normVec := float32(0)
					if maxV > minV {
						normVec = (ch.Score - minV) / (maxV - minV)
					}
					normR := float32(0)
					if maxR > minR {
						normR = (rerankScore - minR) / (maxR - minR)
					}
					fused := wVec*normVec + wRer*normR
					uuid, _ := ch.Payload["submission_uuid"].(string)
					finalScores[uuid] = fused
				}
			}
		}
	}

	// 如果 Reranker 失败或没有任何结果，则回退到纯向量分数
	if len(finalScores) == 0 {
		h.logger.Printf("融合分数为空，回退到纯向量分数排序")
		for _, ch := range candidateChunks {
			uuid, _ := ch.Payload["submission_uuid"].(string)
			finalScores[uuid] = ch.Score
		}
	}

	// 6. 转换、排序、去重
	var ranked []types.RankedSubmission
	for uuid, sc := range finalScores {
		ranked = append(ranked, types.RankedSubmission{SubmissionUUID: uuid, Score: sc})
	}
	sort.Slice(ranked, func(i, j int) bool {
		return ranked[i].Score > ranked[j].Score
	})

	return h.DeduplicateSubmissions(ctx, ranked)
}

// storeResultsInCache 将排序结果缓存到Redis
func (h *JobSearchHandler) storeResultsInCache(ctx context.Context, cacheKey string, results []types.RankedSubmission) error {
	pipe := h.storage.Redis.Client.Pipeline()

	// 先删除旧缓存
	pipe.Del(ctx, cacheKey)

	// 批量添加到ZSET
	for i, r := range results {
		pipe.ZAdd(ctx, cacheKey, redis.Z{
			Score:  float64(len(results) - i), // 使用倒序索引作为分数
			Member: r.SubmissionUUID,
		})
	}

	// 设置过期时间
	pipe.Expire(ctx, cacheKey, 30*time.Minute)

	// 执行管道
	_, err := pipe.Exec(ctx)
	return err
}

// DeduplicateSubmissions a new method for JobSearchHandler to deduplicate submissions by candidate
func (h *JobSearchHandler) DeduplicateSubmissions(ctx context.Context, rankedSubmissions []types.RankedSubmission) ([]types.RankedSubmission, error) {
	if len(rankedSubmissions) == 0 {
		return nil, nil
	}
	var submissionUUIDs []string
	for _, sub := range rankedSubmissions {
		submissionUUIDs = append(submissionUUIDs, sub.SubmissionUUID)
	}

	type submissionToCandidate struct {
		SubmissionUUID string `gorm:"column:submission_uuid"`
		CandidateID    string `gorm:"column:candidate_id"`
	}
	var mappings []submissionToCandidate
	err := h.storage.MySQL.DB().WithContext(ctx).
		Table("resume_submissions").
		Select("submission_uuid", "candidate_id").
		Where("submission_uuid IN ?", submissionUUIDs).
		Find(&mappings).Error
	if err != nil {
		return nil, fmt.Errorf("查询submission到candidate映射失败: %w", err)
	}

	subToCandMap := make(map[string]string)
	for _, m := range mappings {
		subToCandMap[m.SubmissionUUID] = m.CandidateID
	}

	bestSubmissionsForCandidate := make(map[string]types.RankedSubmission)
	// Iterate through the original ranked submissions to preserve order
	for _, sub := range rankedSubmissions {
		candidateID := subToCandMap[sub.SubmissionUUID]
		if candidateID == "" {
			// Fallback for safety, though this should ideally not happen
			candidateID = sub.SubmissionUUID
		}

		if existing, ok := bestSubmissionsForCandidate[candidateID]; !ok || sub.Score > existing.Score {
			bestSubmissionsForCandidate[candidateID] = sub
		}
	}

	var finalSubmissions []types.RankedSubmission
	// The order is lost here, we need to re-sort
	for _, sub := range bestSubmissionsForCandidate {
		finalSubmissions = append(finalSubmissions, sub)
	}
	sort.Slice(finalSubmissions, func(i, j int) bool {
		return finalSubmissions[i].Score > finalSubmissions[j].Score
	})

	return finalSubmissions, nil
}

// batchReranker 批量处理Reranker请求
func (h *JobSearchHandler) batchReranker(ctx context.Context, query string, documents []storage.SearchResult) (map[string]float32, error) {
	// 如果配置禁用了Reranker或没有提供URL，直接返回
	if !h.cfg.Reranker.Enabled || h.cfg.Reranker.URL == "" {
		return nil, nil
	}

	const BatchSize = 100 // 每批次处理的文档数

	// 确定并行工作数量
	cpuCount := runtime.NumCPU()
	workerCount := cpuCount * 2
	if workerCount > 8 {
		workerCount = 8
	}

	// 结果和错误通道
	resultChan := make(chan map[string]float32, workerCount)
	errChan := make(chan error, workerCount)
	var wg sync.WaitGroup

	// 将文档分成多个批次
	var batches [][]storage.SearchResult
	for i := 0; i < len(documents); i += BatchSize {
		end := i + BatchSize
		if end > len(documents) {
			end = len(documents)
		}
		batches = append(batches, documents[i:end])
	}

	// 批次任务通道
	batchChan := make(chan []storage.SearchResult, len(batches))
	for _, batch := range batches {
		batchChan <- batch
	}
	close(batchChan)

	// 启动工作协程
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for batch := range batchChan {
				scores, err := h.processBatch(ctx, query, batch)
				if err != nil {
					select {
					case errChan <- err:
					default:
					}
					return
				}

				if scores != nil {
					select {
					case resultChan <- scores:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	// 等待所有工作完成
	go func() {
		wg.Wait()
		close(resultChan)
		close(errChan)
	}()

	// 合并结果
	mergedScores := make(map[string]float32)
	for scores := range resultChan {
		for id, score := range scores {
			mergedScores[id] = score
		}
	}

	// 检查错误
	select {
	case err := <-errChan:
		if err != nil {
			return nil, err
		}
	default:
	}

	h.logger.Printf("批处理Reranker完成，共处理 %d 批次，获得 %d 个重排分数", len(batches), len(mergedScores))
	return mergedScores, nil
}

// processBatch 处理单个批次的Reranker请求
func (h *JobSearchHandler) processBatch(ctx context.Context, query string, batch []storage.SearchResult) (map[string]float32, error) {
	// 准备请求文档
	var docsToRerank []RerankDocument

	for _, res := range batch {
		// 尝试从多个可能的字段获取内容
		var content string
		var found bool
		possibleFields := []string{"chunk_content_text", "content", "text"}

		for _, field := range possibleFields {
			if textContent, ok := res.Payload[field].(string); ok && textContent != "" {
				content = textContent
				found = true
				break
			}
		}

		// 如果找不到，尝试任意字符串字段
		if !found {
			for _, v := range res.Payload {
				if textContent, ok := v.(string); ok && textContent != "" {
					content = textContent
					found = true
					break
				}
			}
		}

		if found {
			docsToRerank = append(docsToRerank, RerankDocument{
				ID:   res.ID,
				Text: content,
			})
		}
	}

	if len(docsToRerank) == 0 {
		return nil, nil
	}

	// 构造请求
	reqBody := RerankRequest{
		Query:     query,
		Documents: docsToRerank,
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", h.cfg.Reranker.URL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	// 设置超时
	client := &http.Client{Timeout: 30 * time.Second}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 检查响应状态
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Reranker服务返回错误: %d, %s", resp.StatusCode, string(bodyBytes))
	}

	// 解析响应
	var rerankedDocs []RerankedDocument
	if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil {
		return nil, err
	}

	// 提取分数
	rerankScores := make(map[string]float32)
	for _, doc := range rerankedDocs {
		rerankScores[doc.ID] = doc.RerankScore
	}

	return rerankScores, nil
}

// HandleCheckSearchStatus 处理检查搜索状态的请求
func (h *JobSearchHandler) HandleCheckSearchStatus(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("job_id")
	hrIDValue, exists := c.Get("hr_id")
	if !exists {
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取 HR ID 失败"})
		return
	}
	hrID := hrIDValue.(string)

	searchID := fmt.Sprintf(constants.KeySearchSession, jobID, hrID)
	lockKey := fmt.Sprintf(constants.KeySearchLock, jobID, hrID)

	// 检查缓存中是否存在结果
	existsCount, err := h.storage.Redis.Client.Exists(ctx, searchID).Result()
	if err != nil {
		h.logger.Printf("检查 searchID: %s 失败: %v", searchID, err)
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "检查搜索状态失败"})
		return
	}

	if existsCount > 0 {
		// 缓存存在，返回已完成状态
		totalCount, err := h.storage.Redis.Client.ZCard(ctx, searchID).Result()
		if err != nil {
			h.logger.Printf("获取缓存元素数量失败: %v", err)
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取搜索结果数量失败"})
			return
		}

		c.JSON(consts.StatusOK, map[string]interface{}{
			"status":      "completed",
			"total_count": totalCount,
		})
		return
	}

	// 检查搜索锁是否存在
	lockExistsCount, err := h.storage.Redis.Client.Exists(ctx, lockKey).Result()
	if err != nil {
		h.logger.Printf("检查搜索锁状态失败: %v", err)
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "检查搜索状态失败"})
		return
	}

	if lockExistsCount > 0 {
		// 锁存在，表示搜索正在进行
		c.JSON(consts.StatusOK, map[string]interface{}{
			"status":      "processing",
			"message":     "搜索正在处理中",
			"retry_after": 2,
		})
		return
	}

	// 既无锁也无缓存，表示尚未开始搜索或者搜索已过期
	c.JSON(consts.StatusOK, map[string]interface{}{
		"status":  "not_found",
		"message": "未找到搜索结果或结果已过期",
	})
}
