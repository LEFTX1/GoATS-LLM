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
	"ai-agent-go/internal/storage"
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

	// 从查询参数获取cursor和size
	cursorStr := c.Query("cursor")
	sizeStr := c.Query("size")

	cursor := int64(0)
	size := int64(10) // 默认大小

	if cursorStr != "" {
		if val, err := strconv.ParseInt(cursorStr, 10, 64); err == nil {
			cursor = val
		}
	}

	if sizeStr != "" {
		if val, err := strconv.ParseInt(sizeStr, 10, 64); err == nil && val > 0 && val <= 100 {
			size = val
		}
	}

	// 2. 获取HR ID (从上下文中，由认证中间件设置)
	hrID, exists := c.Get("hr_id")
	if !exists {
		c.JSON(consts.StatusUnauthorized, map[string]interface{}{
			"error": "未授权访问",
		})
		return
	}

	h.logger.Printf("开始处理分页查询请求, jobID: %s, hrID: %s, cursor: %d, size: %d",
		jobID, hrID, cursor, size)

	// 3. 生成缓存键 - 与JobSearchHandler使用相同的格式
	// 注意: JobSearchHandler使用的格式是 "search_session:{jobID}"
	cacheKey := fmt.Sprintf("search_session:%s", jobID)
	h.logger.Printf("使用缓存键: %s", cacheKey)

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

	// 批量查询简历基本信息
	type BasicInfoResult struct {
		SubmissionUUID     string
		LLMParsedBasicInfo json.RawMessage
	}

	var basicInfoResults []BasicInfoResult
	err := h.storage.MySQL.DB().WithContext(ctx).
		Raw(`SELECT submission_uuid, llm_parsed_basic_info 
			FROM resume_submissions 
			WHERE submission_uuid IN (?)`, uuids).
		Scan(&basicInfoResults).Error
	if err != nil {
		return nil, fmt.Errorf("查询简历基本信息失败: %w", err)
	}

	// 解析基本信息并创建映射
	basicInfoMap := make(map[string]map[string]string)
	for _, info := range basicInfoResults {
		var basicInfo map[string]string
		if err := json.Unmarshal(info.LLMParsedBasicInfo, &basicInfo); err != nil {
			h.logger.Printf("解析简历 %s 的基本信息失败: %v", info.SubmissionUUID, err)
			basicInfo = map[string]string{} // 使用空映射
		}
		basicInfoMap[info.SubmissionUUID] = basicInfo
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
	resumeMap := make(map[string]*types.ResumeWithChunks)

	for _, chunk := range chunkResults {
		if _, exists := resumeMap[chunk.SubmissionUUID]; !exists {
			// 创建新的简历条目
			resumeMap[chunk.SubmissionUUID] = &types.ResumeWithChunks{
				SubmissionUUID: chunk.SubmissionUUID,
				BasicInfo:      basicInfoMap[chunk.SubmissionUUID],
				Chunks:         []types.ResumeChunkData{},
			}
		}

		// 添加块
		resumeMap[chunk.SubmissionUUID].Chunks = append(
			resumeMap[chunk.SubmissionUUID].Chunks,
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
		if resume, exists := resumeMap[uuid]; exists {
			// 确保每个简历内的块按照ChunkID排序
			sort.Slice(resume.Chunks, func(i, j int) bool {
				return resume.Chunks[i].ChunkID < resume.Chunks[j].ChunkID
			})
			result = append(result, *resume)
		} else {
			// 如果没有找到相应的块，创建一个只有基本信息的条目
			result = append(result, types.ResumeWithChunks{
				SubmissionUUID: uuid,
				BasicInfo:      basicInfoMap[uuid],
				Chunks:         []types.ResumeChunkData{},
			})
		}
	}

	return result, nil
}
