package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"

	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// ResumePaginatedHandler 处理分页简历查询
type ResumePaginatedHandler struct {
	cfg     *config.Config
	storage *storage.Storage
	logger  *log.Logger
}

// NewResumePaginatedHandler 创建分页查询处理器
func NewResumePaginatedHandler(cfg *config.Config, storage *storage.Storage) *ResumePaginatedHandler {
	return &ResumePaginatedHandler{
		cfg:     cfg,
		storage: storage,
		logger:  log.New(os.Stdout, "[ResumePaginated] ", log.LstdFlags),
	}
}

// HandlePaginatedResumeSearch 处理分页简历搜索请求
func (h *ResumePaginatedHandler) HandlePaginatedResumeSearch(ctx context.Context, c *app.RequestContext) {
	// 1. 获取参数
	jobID := c.Param("job_id")
	if jobID == "" {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"error": "job_id 不能为空"})
		return
	}

	// 从认证中间件获取 hr_id
	hrIDValue, exists := c.Get("hr_id")
	if !exists {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"error": "从上下文中获取 hr_id 失败"})
		return
	}
	hrID, ok := hrIDValue.(string)
	if !ok || hrID == "" {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"error": "hr_id 格式不正确或为空"})
		return
	}

	// 统一使用包含 hrID 的缓存键
	cacheKey := fmt.Sprintf(constants.KeySearchSession, jobID, hrID)

	// 获取分页参数
	cursor, err := strconv.ParseInt(c.Query("cursor"), 10, 64)
	if err != nil || cursor < 0 {
		cursor = 0
	}

	sizeStr := c.Query("size")
	size := int64(10) // 默认大小

	if sizeStr != "" {
		if val, err := strconv.ParseInt(sizeStr, 10, 64); err == nil && val > 0 && val <= 100 {
			size = val
		}
	}

	h.logger.Printf("开始处理分页查询请求, jobID: %s, hrID: %s, cursor: %d, size: %d",
		jobID, hrID, cursor, size)

	// 4. 从Redis ZSET获取指定批次的UUID
	uuids, totalCount, err := h.storage.Redis.GetCachedSearchResults(ctx, cacheKey, cursor, size)
	if err != nil {
		h.logger.Printf("获取缓存的搜索结果失败: %v", err)
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{
			"error": "获取搜索结果失败",
		})
		return
	}

	h.logger.Printf("从缓存获取到 %d 个UUID，总数: %d", len(uuids), totalCount)

	// 如果没有结果，直接返回空结果
	if len(uuids) == 0 {
		c.JSON(consts.StatusOK, types.PaginatedResumeResponse{
			JobID:      jobID,
			Cursor:     cursor,
			NextCursor: cursor,
			Size:       size,
			TotalCount: totalCount,
			Resumes:    []types.ResumeWithChunks{},
		})
		return
	}

	// 5. 批量查询MySQL获取简历详情
	resumes, err := h.fetchResumeDetails(ctx, uuids)
	if err != nil {
		h.logger.Printf("获取简历详情失败: %v", err)
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{
			"error": "获取简历详情失败",
		})
		return
	}

	// 6. 计算下一页游标
	nextCursor := cursor + size
	if nextCursor >= totalCount {
		// 如果已经是最后一页，游标保持不变
		nextCursor = cursor
	}

	// 7. 返回结果
	c.JSON(consts.StatusOK, types.PaginatedResumeResponse{
		JobID:      jobID,
		Cursor:     cursor,
		NextCursor: nextCursor,
		Size:       size,
		TotalCount: totalCount,
		Resumes:    resumes,
	})
}

// fetchResumeDetails 获取多个简历的详细信息
func (h *ResumePaginatedHandler) fetchResumeDetails(ctx context.Context, uuids []string) ([]types.ResumeWithChunks, error) {
	if len(uuids) == 0 {
		return []types.ResumeWithChunks{}, nil
	}

	h.logger.Printf("开始获取 %d 个简历的详细信息", len(uuids))

	// 创建结果数组
	result := make([]types.ResumeWithChunks, 0, len(uuids))

	// 批量查询简历基本信息，并预加载关联的 Candidate
	var submissions []models.ResumeSubmission
	err := h.storage.MySQL.DB().WithContext(ctx).
		Preload("Candidate").
		Where("submission_uuid IN (?)", uuids).
		Find(&submissions).Error
	if err != nil {
		return nil, fmt.Errorf("查询简历基本信息失败: %w", err)
	}

	// 将简历基本信息放入 map 中以便快速查找
	submissionMap := make(map[string]models.ResumeSubmission)
	for _, sub := range submissions {
		submissionMap[sub.SubmissionUUID] = sub
	}

	// 批量查询简历块
	type ChunkResult struct {
		SubmissionUUID      string
		ChunkIDInSubmission int
		ChunkType           string
		ChunkTitle          string
		ChunkContentText    string
	}

	var chunkResults []ChunkResult
	err = h.storage.MySQL.DB().WithContext(ctx).
		Raw(`SELECT submission_uuid, chunk_id_in_submission, chunk_type, chunk_title, chunk_content_text 
			FROM resume_submission_chunks 
			WHERE submission_uuid IN (?) 
			ORDER BY submission_uuid, chunk_id_in_submission`, uuids).
		Scan(&chunkResults).Error
	if err != nil {
		return nil, fmt.Errorf("查询简历块失败: %w", err)
	}

	h.logger.Printf("查询到 %d 个简历块", len(chunkResults))

	// 按UUID分组块
	resumeDataMap := make(map[string]*types.ResumeWithChunks)

	for _, chunk := range chunkResults {
		if _, exists := resumeDataMap[chunk.SubmissionUUID]; !exists {
			// 创建新的简历条目
			baseSub, _ := submissionMap[chunk.SubmissionUUID]
			var basicInfo map[string]string
			_ = json.Unmarshal(baseSub.LLMParsedBasicInfo, &basicInfo)

			candidateName := ""
			if baseSub.Candidate != nil {
				candidateName = baseSub.Candidate.PrimaryName
			}

			resumeDataMap[chunk.SubmissionUUID] = &types.ResumeWithChunks{
				SubmissionUUID:   chunk.SubmissionUUID,
				OriginalFilename: baseSub.OriginalFilename,
				CandidateName:    candidateName,
				HighestEducation: baseSub.HighestEducation,
				YearsOfExp:       baseSub.YearsOfExp,
				Is211:            baseSub.Is211,
				Is985:            baseSub.Is985,
				IsDoubleTop:      baseSub.IsDoubleTop,
				BasicInfo:        basicInfo, // 仍然填充旧字段以实现兼容
				Chunks:           []types.ResumeChunkData{},
			}
		}

		// 添加块
		resumeDataMap[chunk.SubmissionUUID].Chunks = append(
			resumeDataMap[chunk.SubmissionUUID].Chunks,
			types.ResumeChunkData{
				ChunkID:     chunk.ChunkIDInSubmission,
				ChunkType:   chunk.ChunkType,
				ChunkTitle:  chunk.ChunkTitle,
				ContentText: chunk.ChunkContentText,
			},
		)
	}

	// 按照传入的UUID顺序组织结果
	for _, uuid := range uuids {
		if resume, exists := resumeDataMap[uuid]; exists {
			// 确保每个简历内的块按照ChunkID排序
			sort.Slice(resume.Chunks, func(i, j int) bool {
				return resume.Chunks[i].ChunkID < resume.Chunks[j].ChunkID
			})
			result = append(result, *resume)
		} else if baseSub, exists := submissionMap[uuid]; exists {
			// 如果简历没有任何块，也要确保它被包括在内
			var basicInfo map[string]string
			_ = json.Unmarshal(baseSub.LLMParsedBasicInfo, &basicInfo)

			candidateName := ""
			if baseSub.Candidate != nil {
				candidateName = baseSub.Candidate.PrimaryName
			}

			result = append(result, types.ResumeWithChunks{
				SubmissionUUID:   uuid,
				OriginalFilename: baseSub.OriginalFilename,
				CandidateName:    candidateName,
				HighestEducation: baseSub.HighestEducation,
				YearsOfExp:       baseSub.YearsOfExp,
				Is211:            baseSub.Is211,
				Is985:            baseSub.Is985,
				IsDoubleTop:      baseSub.IsDoubleTop,
				BasicInfo:        basicInfo,
				Chunks:           []types.ResumeChunkData{},
			})
		}
	}

	return result, nil
}
