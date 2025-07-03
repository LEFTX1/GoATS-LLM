package handler // 定义了此文件属于 handler 包，用于处理业务逻辑的句柄

import ( // 导入一系列标准库和第三方库
	"ai-agent-go/internal/config"         // 导入项目内部的配置包
	"ai-agent-go/internal/constants"      // 导入项目内部的常量包
	"ai-agent-go/internal/processor"      // 导入项目内部的处理器包，例如处理JD
	"ai-agent-go/internal/storage"        // 导入项目内部的存储包，封装了数据库和缓存操作
	"ai-agent-go/internal/storage/models" // 导入数据库模型定义
	"ai-agent-go/internal/types"          // 导入项目内部的类型定义
	"bytes"                               // 提供对 []byte 操作的函数
	"container/heap"                      // 提供堆数据结构的操作
	"context"                             // 提供请求上下文，用于控制goroutine的生命周期和传递请求范围的数据
	"encoding/json"                       // 提供JSON编码和解码功能
	"errors"                              // 提供创建和检查错误的功能
	"fmt"                                 // 提供格式化I/O的功能
	"io"                                  // 提供基本的I/O接口
	"log"                                 // 提供日志记录功能
	"math"                                // 提供基本的数学函数
	"net/http"                            // 提供HTTP客户端和服务器的实现
	"os"                                  // 提供与操作系统交互的功能
	"runtime"                             // 提供与Go运行时交互的功能，如goroutine管理
	"sort"                                // 提供排序算法
	"strconv"                             // 提供字符串和其他基本数据类型之间的转换
	"sync"                                // 提供基本的同步原语，如互斥锁和等待组
	"time"                                // 提供时间相关的操作

	"github.com/cloudwego/hertz/pkg/app"             // 导入Hertz框架的应用相关包
	"github.com/cloudwego/hertz/pkg/protocol/consts" // 导入Hertz框架的常量包，如HTTP状态码
	"github.com/redis/go-redis/v9"                   // 导入go-redis客户端库
	"gorm.io/gorm"                                   // 导入GORM库，一个Go语言的ORM
)

// JobSearchHandler 负责处理与职位相关的搜索请求。
type JobSearchHandler struct { // 定义 JobSearchHandler 结构体
	cfg         *config.Config         // cfg 存储应用的配置信息
	storage     *storage.Storage       // storage 提供对数据库和缓存的访问
	jdProcessor *processor.JDProcessor // jdProcessor 用于处理岗位描述（JD）相关的逻辑
	logger      *log.Logger            // logger 用于记录日志
}

// NewJobSearchHandler 创建一个新的 JobSearchHandler 实例。
func NewJobSearchHandler(cfg *config.Config, storage *storage.Storage, jdProcessor *processor.JDProcessor) *JobSearchHandler { // NewJobSearchHandler 是 JobSearchHandler 的构造函数
	return &JobSearchHandler{ // 返回一个新的 JobSearchHandler 实例指针
		cfg:         cfg,                                                                     // 初始化配置
		storage:     storage,                                                                 // 初始化存储
		jdProcessor: jdProcessor,                                                             // 初始化JD处理器
		logger:      log.New(os.Stdout, "[JobSearchHandler] ", log.LstdFlags|log.Lshortfile), // 初始化日志记录器，输出到标准输出，带前缀、标准时间和短文件名
	}
}

// HandleSearchResumesByJobID 处理根据 JobID 搜索简历的请求。
// GET /api/v1/jobs/:job_id/resumes/search
func (h *JobSearchHandler) HandleSearchResumesByJobID(ctx context.Context, c *app.RequestContext) { // 定义处理简历搜索请求的方法
	// 1. 获取请求参数
	jobID := c.Param("job_id") // 从URL路径参数中获取 job_id
	if jobID == "" {           // 检查 job_id 是否为空
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "job_id 不能为空"}) // 如果为空，返回400错误
		return                                                                     // 结束函数执行
	}

	// 从认证中间件获取 hr_id
	hrIDValue, exists := c.Get("hr_id") // 从请求上下文中获取 hr_id，这个值通常由认证中间件设置
	if !exists {                        // 检查 hr_id 是否存在
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取 HR ID 失败"}) // 如果不存在，返回500错误
		return                                                                              // 结束函数执行
	}
	hrID := hrIDValue.(string) // 将 hr_id 转换为字符串类型

	// 获取分页参数
	displayLimitStr := c.Query("display_limit")        // 从查询参数中获取 display_limit
	displayLimit, err := strconv.Atoi(displayLimitStr) // 将字符串类型的 display_limit 转换为整数
	if err != nil || displayLimit <= 0 {               // 如果转换失败或值不合法
		displayLimit = 20 // 设置默认值为20
	}

	cursorStr := c.Query("cursor")         // 从查询参数中获取 cursor
	cursor, err := strconv.Atoi(cursorStr) // 将字符串类型的 cursor 转换为整数
	if err != nil || cursor < 0 {          // 如果转换失败或值不合法
		cursor = 0 // 设置默认值为0
	}

	h.logger.Printf("开始处理 JobID: %s 的简历搜索请求, HR: %s, Limit: %d, Cursor: %d", jobID, hrID, displayLimit, cursor) // 记录日志，说明开始处理请求

	// 2. 生成唯一的、可复现的搜索会话ID和锁ID，添加HR维度
	searchID := fmt.Sprintf(constants.KeySearchSession, jobID, hrID) // 使用 jobID 和 hrID 生成唯一的搜索会话ID
	lockKey := fmt.Sprintf(constants.KeySearchLock, jobID, hrID)     // 使用 jobID 和 hrID 生成唯一的分布式锁键

	// 3. 检查缓存
	// 尝试从Redis缓存中获取"黄金结果集"的分页数据
	cachedUUIDs, totalCount, err := h.storage.Redis.GetCachedSearchResults(ctx, searchID, int64(cursor), int64(displayLimit)) // 调用Redis存储层获取缓存的搜索结果
	if err == nil && len(cachedUUIDs) > 0 {                                                                                   // 如果没有错误且获取到了数据
		h.logger.Printf("缓存命中 for searchID: %s. 返回 %d 个简历UUIDs。", searchID, len(cachedUUIDs)) // 记录缓存命中日志
		// 缓存命中，直接从数据库查询这些UUID的详细信息并返回
		detailedResumes, dbErr := h.fetchResumeDetailsByUUIDs(ctx, cachedUUIDs) // 根据UUIDs从数据库获取简历详情
		if dbErr != nil {                                                       // 如果数据库查询出错
			h.logger.Printf("从数据库查询 Submission 详情失败 (UUIDs: %v): %v", cachedUUIDs, dbErr)    // 记录错误日志
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取简历详情失败"}) // 返回500错误
			return                                                                           // 结束函数执行
		}
		c.JSON(consts.StatusOK, map[string]interface{}{ // 返回200成功响应
			"message":     "搜索成功 (来自缓存)",             // 消息
			"data":        detailedResumes,           // 简历数据
			"job_id":      jobID,                     // 职位ID
			"total_count": totalCount,                // 结果总数
			"next_cursor": cursor + len(cachedUUIDs), // 下一页的游标
		})
		return // 结束函数执行
	}

	// 4. 缓存未命中且为首次请求，执行完整搜索流程
	if cursor == 0 { // 检查是否是第一次请求（游标为0）
		h.logger.Printf("缓存未命中 for searchID: %s, 执行完整搜索流程...", searchID) // 记录缓存未命中日志

		// 4.1 尝试获取分布式锁，避免并发执行相同搜索
		lockValue, err := h.storage.Redis.AcquireLock(ctx, lockKey, 5*time.Minute) // 尝试获取一个5分钟过期的分布式锁
		if err != nil {                                                            // 如果获取锁时发生错误
			h.logger.Printf("获取分布式锁失败 for JobID %s: %v，继续执行可能导致重复处理", jobID, err) // 记录日志，但允许继续执行
		} else if lockValue == "" { // 如果未能获取到锁（已被其他请求持有）
			// 未能获取锁，表示已有其他进程正在处理相同搜索
			h.logger.Printf("搜索已在处理中 for JobID: %s，返回等待消息", jobID) // 记录日志
			c.JSON(consts.StatusAccepted, map[string]interface{}{  // 返回202接受状态
				"message":     "您的搜索请求正在处理中，请稍后重试", // 提示信息
				"status":      "processing",        // 状态
				"job_id":      jobID,               // 职位ID
				"retry_after": 2,                   // 建议客户端2秒后重试
			})
			return // 结束函数执行
		} else { // 成功获取到锁
			// 成功获取锁，在函数结束时释放
			defer func() { // 使用 defer 确保函数退出时执行
				released, err := h.storage.Redis.ReleaseLock(ctx, lockKey, lockValue) // 释放锁
				if err != nil || !released {                                          // 如果释放锁失败
					h.logger.Printf("释放锁失败 for JobID: %s: %v, released: %v", jobID, err, released) // 记录日志
				} else { // 成功释放锁
					h.logger.Printf("成功释放锁 for JobID: %s", jobID) // 记录日志
				}
			}()
		}

		// 执行完整的召回、重排、聚合流程
		finalRankedSubmissions, err := h.executeFullSearchPipeline(ctx, jobID, hrID) // 调用完整搜索流程函数
		if err != nil {                                                              // 如果搜索流程出错
			h.logger.Printf("完整搜索流程失败 for JobID %s: %v", jobID, err)                       // 记录错误日志
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "执行搜索失败"}) // 返回500错误
			return                                                                         // 结束函数执行
		}

		if len(finalRankedSubmissions) == 0 { // 如果没有找到任何结果
			h.logger.Printf("完整搜索流程 for JobID %s 未找到任何结果", jobID)                                            // 记录日志
			c.JSON(consts.StatusOK, map[string]interface{}{"message": "没有找到匹配的简历", "data": []interface{}{}}) // 返回空的成功响应
			return                                                                                           // 结束函数执行
		}

		// 将"黄金结果集"写入缓存
		err = h.storeResultsInCache(ctx, searchID, finalRankedSubmissions) // 将最终结果存入Redis缓存
		if err != nil {                                                    // 如果缓存失败
			// 只记录日志，不阻塞主流程
			h.logger.Printf("缓存黄金结果集失败 for searchID %s: %v", searchID, err) // 记录错误日志
		}

		// 从刚生成的"黄金结果集"中取第一页数据
		pageUUIDs := make([]string, 0, displayLimit) // 创建一个切片用于存放当前页的UUID
		end := cursor + displayLimit                 // 计算当前页的结束索引
		if end > len(finalRankedSubmissions) {       // 如果结束索引超出总数
			end = len(finalRankedSubmissions) // 将结束索引设为总数
		}

		for i := cursor; i < end; i++ { // 遍历当前页的结果
			pageUUIDs = append(pageUUIDs, finalRankedSubmissions[i].SubmissionUUID) // 将UUID添加到切片中
		}

		// 查询详情并返回
		detailedResumes, dbErr := h.fetchResumeDetailsByUUIDs(ctx, pageUUIDs) // 根据UUIDs获取简历详情
		if dbErr != nil {                                                     // 如果获取详情失败
			h.logger.Printf("从数据库查询 Submission 详情失败 (UUIDs: %v): %v", pageUUIDs, dbErr)      // 记录错误日志
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取简历详情失败"}) // 返回500错误
			return                                                                           // 结束函数执行
		}
		c.JSON(consts.StatusOK, map[string]interface{}{ // 返回200成功响应
			"message":     "搜索成功",                        // 消息
			"data":        detailedResumes,               // 简历数据
			"job_id":      jobID,                         // 职位ID
			"total_count": len(finalRankedSubmissions),   // 结果总数
			"next_cursor": cursor + len(detailedResumes), // 下一页的游标
		})
		return // 结束函数执行
	}

	// 5. 非首次请求但缓存失效或已读完，返回空
	h.logger.Printf("非首次请求但缓存未命中 for searchID: %s, Cursor: %d. 可能已浏览完所有结果。", searchID, cursor) // 记录日志
	c.JSON(consts.StatusOK, map[string]interface{}{                                            // 返回200成功响应
		"message":     "已查看所有匹配的简历",    // 消息
		"data":        []interface{}{}, // 空数据
		"job_id":      jobID,           // 职位ID
		"total_count": totalCount,      // 从缓存查询中获取的总数
		"next_cursor": cursor,          // 游标不变
	})
}

// fetchResumeDetailsByUUIDs 是一个辅助函数，用于根据UUID列表从数据库批量获取简历详情
func (h *JobSearchHandler) fetchResumeDetailsByUUIDs(ctx context.Context, uuids []string) ([]map[string]interface{}, error) { // 定义通过UUID批量获取简历详情的方法
	if len(uuids) == 0 { // 如果UUID列表为空
		return []map[string]interface{}{}, nil // 返回空的map切片和nil错误
	}

	var submissions []models.ResumeSubmission // 声明一个切片用于存储查询结果
	// 使用 Preload 加载关联的 Candidate 信息
	err := h.storage.MySQL.DB().WithContext(ctx). // 使用带上下文的数据库连接
							Preload("Candidate").                 // 预加载关联的Candidate表
							Where("submission_uuid IN ?", uuids). // 设置查询条件，查找UUID在列表中的记录
							Find(&submissions).Error              // 执行查询并将结果存入submissions切片
	if err != nil { // 如果查询出错
		return nil, err // 返回nil和错误
	}

	// 为了保持返回顺序与传入的uuids一致，创建一个map
	submissionMap := make(map[string]models.ResumeSubmission, len(submissions)) // 创建一个map，用于快速查找submission
	for _, sub := range submissions {                                           // 遍历查询结果
		submissionMap[sub.SubmissionUUID] = sub // 以UUID为键，submission为值存入map
	}

	var detailedResumes []map[string]interface{} // 声明一个切片用于存放最终的详细简历信息
	for _, uuid := range uuids {                 // 遍历传入的UUID列表，以保持顺序
		if sub, ok := submissionMap[uuid]; ok { // 从map中查找对应的submission
			candidateName := ""       // 初始化候选人姓名
			candidateEmail := ""      // 初始化候选人邮箱
			candidatePhone := ""      // 初始化候选人电话
			if sub.Candidate != nil { // 如果关联的Candidate不为空
				candidateName = sub.Candidate.PrimaryName   // 获取候选人姓名
				candidateEmail = sub.Candidate.PrimaryEmail // 获取候选人邮箱
				candidatePhone = sub.Candidate.PrimaryPhone // 获取候选人电话
			}

			detailedResumes = append(detailedResumes, map[string]interface{}{ // 将简历详情添加到结果切片中
				"submission_uuid":   sub.SubmissionUUID,                                    // 投递UUID
				"file_name":         sub.OriginalFilename,                                  // 原始文件名
				"source_channel":    sub.SourceChannel,                                     // 来源渠道
				"upload_time":       sub.SubmissionTimestamp.Format("2006-01-02 15:04:05"), // 投递时间，格式化输出
				"candidate_name":    candidateName,                                         // 候选人姓名
				"candidate_email":   candidateEmail,                                        // 候选人邮箱
				"candidate_phone":   candidatePhone,                                        // 候选人电话
				"processing_status": sub.ProcessingStatus,                                  // 处理状态
				// 这里暂时不返回分数和qdrant payload，因为它们存储在缓存中，需要额外逻辑获取
			})
		}
	}

	return detailedResumes, nil // 返回详细简历列表和nil错误
}

// getJobDescription 辅助函数，用于获取岗位描述文本。
// 首先尝试从 Redis 缓存获取，如果缓存未命中，则从 MySQL 数据库查询，并回填缓存。
func (h *JobSearchHandler) getJobDescription(ctx context.Context, jobID string) (string, error) { // 定义获取JD文本的方法
	cacheKey := fmt.Sprintf(constants.KeyJobDescriptionText, jobID) // 生成JD文本的Redis缓存键

	// 1. 尝试从 Redis 缓存获取 JD 文本
	cachedJD, err := h.storage.Redis.Get(ctx, cacheKey) // 从Redis获取缓存
	if err == nil && cachedJD != "" {                   // 如果没有错误且缓存存在
		h.logger.Printf("从 Redis 缓存命中 JD 文本 for JobID: %s", jobID) // 记录缓存命中日志
		return cachedJD, nil                                       // 返回缓存的JD文本
	}
	if err != nil { // 如果获取缓存时发生错误
		// 如果是 redis.Nil 之外的错误，记录一下，但继续从数据库获取
		if !errors.Is(err, storage.ErrNotFound) { // 判断是否是"未找到"之外的错误
			h.logger.Printf("从 Redis 获取 JD 文本缓存失败 for JobID: %s: %v", jobID, err) // 记录其他Redis错误
		}
	}

	// 2. 缓存未命中，从 MySQL 直接获取
	h.logger.Printf("Redis 缓存未命中 JD 文本 for JobID: %s，从 MySQL 查询", jobID)                     // 记录缓存未命中日志
	var job models.Job                                                                       // 声明一个Job模型变量
	err = h.storage.MySQL.DB().WithContext(ctx).Where("job_id = ?", jobID).First(&job).Error // 从MySQL查询指定jobID的记录
	if err != nil {                                                                          // 如果查询出错
		return "", err // 返回空字符串和错误，包括 gorm.ErrRecordNotFound
	}

	if job.JobDescriptionText == "" { // 如果JD文本为空
		return "", fmt.Errorf("job_id %s 对应的岗位描述为空", jobID) // 返回错误信息
	}

	// 3. 获取到 JD 后，将其存入 Redis 缓存
	// 使用一个合理的过期时间，例如 24 小时
	cacheDuration := 24 * time.Hour                                                 // 设置缓存过期时间为24小时
	err = h.storage.Redis.Set(ctx, cacheKey, job.JobDescriptionText, cacheDuration) // 将JD文本存入Redis
	if err != nil {                                                                 // 如果设置缓存失败
		// 缓存失败不应阻塞主流程，但需要记录日志
		h.logger.Printf("将 JD 文本存入 Redis 失败 for JobID: %s: %v", jobID, err) // 记录错误日志
	} else { // 成功设置缓存
		h.logger.Printf("成功将 JD 文本存入 Redis 缓存 for JobID: %s", jobID) // 记录成功日志
	}

	return job.JobDescriptionText, nil // 返回从数据库获取的JD文本
}

// HandleGetJobDescription 处理获取特定岗位描述的请求 (辅助接口，可选)
// GET /api/v1/jobs/:job_id/description
func (h *JobSearchHandler) HandleGetJobDescription(ctx context.Context, c *app.RequestContext) { // 定义获取JD的API处理函数
	jobID := c.Param("job_id") // 从URL路径参数获取job_id
	if jobID == "" {           // 检查job_id是否为空
		c.JSON(consts.StatusBadRequest, map[string]string{"error": "job_id 不能为空"}) // 返回400错误
		return                                                                     // 结束函数执行
	}

	jdText, err := h.getJobDescription(ctx, jobID) // 调用辅助函数获取JD文本
	if err != nil {                                // 如果获取JD时出错
		if errors.Is(err, gorm.ErrRecordNotFound) { // 判断是否是记录未找到的错误
			c.JSON(consts.StatusNotFound, map[string]string{"error": fmt.Sprintf("未找到 JobID 为 %s 的岗位", jobID)}) // 返回404错误
		} else { // 其他错误
			h.logger.Printf("获取 JobID: %s 的岗位描述失败: %v", jobID, err)                          // 记录错误日志
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取岗位描述失败"}) // 返回500错误
		}
		return // 结束函数执行
	}

	// 尝试从 Redis 获取 JD 向量信息（如果存在）
	vector, modelVersion, err := h.storage.Redis.GetJobVector(ctx, jobID) // 从Redis获取JD向量
	var vectorDim int                                                     // 声明变量存储向量维度
	if err == nil && len(vector) > 0 {                                    // 如果没有错误且向量不为空
		vectorDim = len(vector) // 获取向量维度
	}

	// 尝试从 MySQL 获取 JobVector 信息
	dbJobVector, dbErr := h.storage.MySQL.GetJobVectorByID(h.storage.MySQL.DB().WithContext(ctx), jobID) // 从MySQL获取JD向量信息
	var dbVectorDim int                                                                                  // 声明变量存储数据库中向量的维度
	var dbModelVersion string                                                                            // 声明变量存储数据库中向量的模型版本
	if dbErr == nil && dbJobVector != nil && len(dbJobVector.VectorRepresentation) > 0 {                 // 如果查询成功且向量不为空
		// 假设 VectorRepresentation 是序列化的 float32 数组
		// 这里只是演示，实际反序列化可能更复杂
		var tempVec []float32                                                  // 声明一个临时float32切片
		if json.Unmarshal(dbJobVector.VectorRepresentation, &tempVec) == nil { // 尝试反序列化向量表示
			dbVectorDim = len(tempVec) // 获取向量维度
		}
		dbModelVersion = dbJobVector.EmbeddingModelVersion // 获取模型版本
	}

	c.JSON(consts.StatusOK, map[string]interface{}{ // 返回200成功响应
		"job_id":              jobID,          // 职位ID
		"description":         jdText,         // 岗位描述文本
		"description_len":     len(jdText),    // 文本长度
		"cached_vector_dim":   vectorDim,      // 缓存中向量的维度
		"cached_vector_model": modelVersion,   // 缓存中向量的模型版本
		"db_vector_dim":       dbVectorDim,    // 数据库中向量的维度
		"db_vector_model":     dbModelVersion, // 数据库中向量的模型版本
	})
}

// 用于高效获取TopK的最小堆
type MinHeap struct { // 定义一个最小堆结构体
	values []float32 // 堆中存储的元素，是一个float32切片
}

// Len 实现heap.Interface的Len()方法
func (h MinHeap) Len() int { return len(h.values) } // Len 返回堆中元素的数量

// Less 实现heap.Interface的Less()方法
func (h MinHeap) Less(i, j int) bool { return h.values[i] < h.values[j] } // Less 比较两个元素，用于维持最小堆的性质

// Swap 实现heap.Interface的Swap()方法
func (h MinHeap) Swap(i, j int) { h.values[i], h.values[j] = h.values[j], h.values[i] } // Swap 交换两个元素的位置

// Push 实现heap.Interface的Push()方法
func (h *MinHeap) Push(x interface{}) { // Push 向堆中添加一个元素
	h.values = append(h.values, x.(float32)) // 将元素添加到切片末尾
}

// Pop 实现heap.Interface的Pop()方法
func (h *MinHeap) Pop() interface{} { // Pop 从堆中移除并返回最小的元素
	old := h.values         // 引用原始切片
	n := len(old)           // 获取切片长度
	x := old[n-1]           // 获取最后一个元素
	h.values = old[0 : n-1] // 缩容切片，移除最后一个元素
	return x                // 返回被移除的元素
}

// GetTopKSum 获取切片中最大的k个元素之和，不修改原切片
// 导出此函数，以便基准测试和扩展使用
func GetTopKSum(scores []float32, k int) (float32, int) { // 定义获取TopK元素之和的函数
	if len(scores) == 0 { // 如果分数切片为空
		return 0, 0 // 返回0和0
	}

	kk := k              // 初始化实际的k值
	if len(scores) < k { // 如果分数数量小于k
		kk = len(scores) // 将k值设为分数的总数
	}

	// 使用最小堆获取TopK
	h := &MinHeap{values: make([]float32, 0, kk)} // 创建一个容量为kk的最小堆

	// 首先填充k个元素
	for i := 0; i < len(scores) && i < kk; i++ { // 遍历前kk个分数
		heap.Push(h, scores[i]) // 将分数推入最小堆
	}

	// 替换较小的元素
	for i := kk; i < len(scores); i++ { // 遍历剩余的分数
		if scores[i] > h.values[0] { // 如果当前分数大于堆顶的最小元素
			heap.Pop(h)             // 弹出堆顶
			heap.Push(h, scores[i]) // 将当前分数推入堆中
		}
	}

	// 计算总和
	var sum float32              // 声明一个变量用于存储总和
	for _, v := range h.values { // 遍历堆中的所有元素
		sum += v // 累加求和
	}

	return sum, kk // 返回TopK元素的总和和实际的k值
}

// ComputeStats 一次遍历计算均值、标准差和覆盖率
// 导出此函数，以便基准测试和扩展使用
func ComputeStats(scores []float32, coverTh float32) (mean float64, stdev float64, cover float32) { // 定义计算统计量的函数
	if len(scores) == 0 { // 如果分数切片为空
		return 0, 0, 0 // 返回0, 0, 0
	}

	var sum, sqSum float64 // 声明变量用于存储总和和平方和
	var coverCount int     // 声明变量用于存储覆盖率计数

	// 一次遍历计算所有统计量
	for _, s := range scores { // 遍历所有分数
		sum += float64(s)                // 累加总和
		sqSum += float64(s) * float64(s) // 累加平方和
		if s >= coverTh {                // 如果分数大于等于覆盖率阈值
			coverCount++ // 覆盖率计数加一
		}
	}

	n := float64(len(scores))   // 获取分数的总数
	mean = sum / n              // 计算均值
	stdev = sqSum/n - mean*mean // 计算方差 (E[X²] - E[X]²)
	if stdev < 0 {              // 避免数值精度问题导致负方差
		stdev = 0 // 将标准差设为0
	} else { // 否则
		stdev = math.Sqrt(stdev) // 计算标准差
	}

	cover = float32(coverCount) / float32(len(scores)) // 计算覆盖率
	return mean, stdev, cover                          // 返回均值、标准差和覆盖率
}

// RerankDocument Reranker服务的输入文档结构
type RerankDocument struct { // 定义Reranker服务的输入文档结构体
	ID   string `json:"id"`   // 文档ID
	Text string `json:"text"` // 文档内容
}

// RerankRequest Reranker服务的请求结构
type RerankRequest struct { // 定义Reranker服务的请求结构体
	Query     string           `json:"query"`     // 查询语句
	Documents []RerankDocument `json:"documents"` // 待重排的文档列表
}

// RerankedDocument Reranker服务的输出文档结构
type RerankedDocument struct { // 定义Reranker服务的输出文档结构体
	ID          string  `json:"id"`           // 文档ID
	RerankScore float32 `json:"rerank_score"` // 重排后的分数
}

// executeFullSearchPipeline 执行完整的搜索流水线
func (h *JobSearchHandler) executeFullSearchPipeline(ctx context.Context, jobID string, hrID string) ([]types.RankedSubmission, error) { // 定义执行完整搜索流程的方法
	// 1. 获取JD文本和向量
	jdText, err := h.getJobDescription(ctx, jobID) // 获取岗位描述文本
	if err != nil {                                // 如果获取失败
		return nil, fmt.Errorf("获取JD失败: %w", err) // 返回错误信息
	}
	jdVector, err := h.jdProcessor.GetJobDescriptionVector(ctx, jobID, jdText) // 获取岗位描述的向量表示
	if err != nil {                                                            // 如果获取失败
		return nil, fmt.Errorf("获取JD向量失败: %w", err) // 返回错误信息
	}

	// 2. 向量数据库召回：场景 C.2 用更大的 K
	const recallLimit = 500                                                                       // 定义向量召回的数量上限
	initialResults, err := h.storage.Qdrant.SearchSimilarResumes(ctx, jdVector, recallLimit, nil) // 在Qdrant中搜索相似的简历
	if err != nil {                                                                               // 如果搜索失败
		return nil, err // 返回错误
	}
	h.logger.Printf("向量召回获得 %d 个初步 chunk", len(initialResults)) // 记录日志

	// 3. 候选人级聚合：每个 submission_uuid 选最高向量分数的 chunk
	bestPerResume := make(map[string]storage.SearchResult) // 创建一个map用于存储每个简历的最佳chunk
	for _, res := range initialResults {                   // 遍历初步召回的结果
		uuid, _ := res.Payload["submission_uuid"].(string) // 从payload中获取submission_uuid
		if uuid == "" {                                    // 如果uuid为空
			continue // 跳过当前结果
		}
		if exist, ok := bestPerResume[uuid]; !ok || res.Score > exist.Score { // 如果当前简历还没有记录，或者当前chunk的分数更高
			bestPerResume[uuid] = res // 更新该简历的最佳chunk
		}
	}
	// 转 slice 并按向量分数降序，截取 Top 120
	var candidateChunks []storage.SearchResult // 声明一个切片用于存储聚合后的候选chunk
	for _, v := range bestPerResume {          // 遍历map中的值
		candidateChunks = append(candidateChunks, v) // 将最佳chunk添加到切片中
	}
	sort.Slice(candidateChunks, func(i, j int) bool { // 对候选chunk按分数降序排序
		return candidateChunks[i].Score > candidateChunks[j].Score // 比较函数
	})
	const candidateTopN = 120                 // 定义进入Rerank阶段的候选简历数量
	if len(candidateChunks) > candidateTopN { // 如果候选数量超过上限
		candidateChunks = candidateChunks[:candidateTopN] // 截取前TopN个
	}
	h.logger.Printf("聚合后，%d 份候选简历（最佳 chunk）进入 Rerank", len(candidateChunks)) // 记录日志

	// 4. 对候选 chunk 批量调用 Reranker
	rerankScores, err := h.batchReranker(ctx, jdText, candidateChunks) // 批量调用Reranker服务
	if err != nil {                                                    // 如果调用失败
		h.logger.Printf("Reranker 调用失败，退回向量排序：%v", err) // 记录错误日志
		rerankScores = nil                              // 将rerank分数设为nil，即使reranker失败，后续也可以只用向量分数
	}

	// 5. 线性融合 (Min-Max 归一化 + 30% 向量分 + 70% Rerank 分)
	finalScores := make(map[string]float32, len(candidateChunks)) // 创建一个map用于存储最终的融合分数

	if rerankScores != nil { // 如果Reranker调用成功
		// 5.1 先收集两组原始分数
		vecScores := make([]float32, 0, len(candidateChunks)) // 创建切片存储向量分数
		rerScores := make([]float32, 0, len(candidateChunks)) // 创建切片存储Rerank分数
		for _, ch := range candidateChunks {                  // 遍历候选chunk
			if _, ok := rerankScores[ch.ID]; ok { // 确保该chunk有对应的rerank分数
				vecScores = append(vecScores, ch.Score)            // 添加向量分数
				rerScores = append(rerScores, rerankScores[ch.ID]) // 添加rerank分数
			}
		}

		if len(vecScores) > 0 { // 如果有有效的分数
			// 5.2 找 min/max
			minMax := func(arr []float32) (min, max float32) { // 定义一个匿名函数用于找切片的最小值和最大值
				min, max = arr[0], arr[0] // 初始化min和max
				for _, v := range arr {   // 遍历切片
					if v < min { // 如果当前值更小
						min = v // 更新min
					}
					if v > max { // 如果当前值更大
						max = v // 更新max
					}
				}
				return // 返回min和max
			}
			minV, maxV := minMax(vecScores) // 获取向量分数的min和max
			minR, maxR := minMax(rerScores) // 获取Rerank分数的min和max

			// 5.3 对每个候选人做归一化并融合
			const wVec, wRer = 0.3, 0.7          // 定义向量分数和Rerank分数的权重
			for _, ch := range candidateChunks { // 遍历候选chunk
				if rerankScore, ok := rerankScores[ch.ID]; ok { // 获取对应的rerank分数
					normVec := float32(0) // 初始化归一化后的向量分数
					if maxV > minV {      // 避免除以零
						normVec = (ch.Score - minV) / (maxV - minV) // Min-Max归一化
					}
					normR := float32(0) // 初始化归一化后的Rerank分数
					if maxR > minR {    // 避免除以零
						normR = (rerankScore - minR) / (maxR - minR) // Min-Max归一化
					}
					fused := wVec*normVec + wRer*normR                // 线性加权融合
					uuid, _ := ch.Payload["submission_uuid"].(string) // 获取submission_uuid
					finalScores[uuid] = fused                         // 存储最终分数
				}
			}
		}
	}

	// 如果 Reranker 失败或没有任何结果，则回退到纯向量分数
	if len(finalScores) == 0 { // 如果最终分数map为空
		h.logger.Printf("融合分数为空，回退到纯向量分数排序") // 记录日志
		for _, ch := range candidateChunks { // 遍历候选chunk
			uuid, _ := ch.Payload["submission_uuid"].(string) // 获取submission_uuid
			finalScores[uuid] = ch.Score                      // 使用原始向量分数作为最终分数
		}
	}

	// 6. 转换、排序、去重
	var ranked []types.RankedSubmission // 声明一个切片用于存储排序后的结果
	for uuid, sc := range finalScores { // 遍历最终分数map
		ranked = append(ranked, types.RankedSubmission{SubmissionUUID: uuid, Score: sc}) // 转换为RankedSubmission结构并添加到切片
	}
	sort.Slice(ranked, func(i, j int) bool { // 对结果按分数降序排序
		return ranked[i].Score > ranked[j].Score // 比较函数
	})

	return h.DeduplicateSubmissions(ctx, ranked) // 返回去重后的最终结果
}

// storeResultsInCache 将排序结果缓存到Redis
func (h *JobSearchHandler) storeResultsInCache(ctx context.Context, cacheKey string, results []types.RankedSubmission) error { // 定义将结果存入缓存的方法
	pipe := h.storage.Redis.Client.Pipeline() // 创建一个Redis管道

	// 先删除旧缓存
	pipe.Del(ctx, cacheKey) // 在管道中添加删除命令

	// 批量添加到ZSET
	for i, r := range results { // 遍历排序后的结果
		pipe.ZAdd(ctx, cacheKey, redis.Z{ // 在管道中添加ZADD命令
			Score:  float64(len(results) - i), // 使用倒序索引作为分数，分数越高排名越靠前
			Member: r.SubmissionUUID,          // 成员是submission_uuid
		})
	}

	// 设置过期时间
	pipe.Expire(ctx, cacheKey, 30*time.Minute) // 在管道中添加设置过期时间的命令

	// 执行管道
	_, err := pipe.Exec(ctx) // 执行管道中的所有命令
	return err               // 返回执行结果的错误
}

// DeduplicateSubmissions a new method for JobSearchHandler to deduplicate submissions by candidate
func (h *JobSearchHandler) DeduplicateSubmissions(ctx context.Context, rankedSubmissions []types.RankedSubmission) ([]types.RankedSubmission, error) { // 定义按候选人去重的方法
	if len(rankedSubmissions) == 0 { // 如果输入为空
		return nil, nil // 返回nil
	}
	var submissionUUIDs []string            // 声明一个切片用于存储所有submission的UUID
	for _, sub := range rankedSubmissions { // 遍历输入的投递列表
		submissionUUIDs = append(submissionUUIDs, sub.SubmissionUUID) // 将UUID添加到切片中
	}

	type submissionToCandidate struct { // 定义一个临时结构体用于存储UUID到候选人ID的映射
		SubmissionUUID string `gorm:"column:submission_uuid"` // 投递UUID
		CandidateID    string `gorm:"column:candidate_id"`    // 候选人ID
	}
	var mappings []submissionToCandidate          // 声明一个切片用于存储查询结果
	err := h.storage.MySQL.DB().WithContext(ctx). // 使用带上下文的数据库连接
							Table("resume_submissions").                    // 指定查询的表
							Select("submission_uuid", "candidate_id").      // 选择要查询的列
							Where("submission_uuid IN ?", submissionUUIDs). // 设置查询条件
							Find(&mappings).Error                           // 执行查询
	if err != nil { // 如果查询失败
		return nil, fmt.Errorf("查询submission到candidate映射失败: %w", err) // 返回错误信息
	}

	subToCandMap := make(map[string]string) // 创建一个map用于存储投递UUID到候选人ID的映射
	for _, m := range mappings {            // 遍历查询结果
		subToCandMap[m.SubmissionUUID] = m.CandidateID // 填充map
	}

	bestSubmissionsForCandidate := make(map[string]types.RankedSubmission) // 创建一个map用于存储每个候选人的最佳投递
	// Iterate through the original ranked submissions to preserve order
	for _, sub := range rankedSubmissions { // 遍历原始排序的投递列表以保持顺序
		candidateID := subToCandMap[sub.SubmissionUUID] // 查找该投递对应的候选人ID
		if candidateID == "" {                          // 如果找不到候选人ID
			// Fallback for safety, though this should ideally not happen
			candidateID = sub.SubmissionUUID // 为安全起见，使用投递UUID本身作为ID
		}

		if existing, ok := bestSubmissionsForCandidate[candidateID]; !ok || sub.Score > existing.Score { // 如果该候选人还没有记录，或者当前投递的分数更高
			bestSubmissionsForCandidate[candidateID] = sub // 更新该候选人的最佳投递
		}
	}

	var finalSubmissions []types.RankedSubmission // 声明一个切片用于存储最终的去重结果
	// The order is lost here, we need to re-sort
	for _, sub := range bestSubmissionsForCandidate { // 遍历最佳投递map
		finalSubmissions = append(finalSubmissions, sub) // 将结果添加到切片中
	}
	sort.Slice(finalSubmissions, func(i, j int) bool { // 对最终结果重新排序，因为map遍历是无序的
		return finalSubmissions[i].Score > finalSubmissions[j].Score // 按分数降序排序
	})

	return finalSubmissions, nil // 返回最终的去重并排序后的投递列表
}

// batchReranker 批量处理Reranker请求
func (h *JobSearchHandler) batchReranker(ctx context.Context, query string, documents []storage.SearchResult) (map[string]float32, error) { // 定义批量调用Reranker的方法
	// 如果配置禁用了Reranker或没有提供URL，直接返回
	if !h.cfg.Reranker.Enabled || h.cfg.Reranker.URL == "" { // 检查配置是否启用Reranker
		return nil, nil // 如果未启用，直接返回nil
	}

	const BatchSize = 100 // 每批次处理的文档数

	// 确定并行工作数量
	cpuCount := runtime.NumCPU() // 获取CPU核心数
	workerCount := cpuCount * 2  // 设置工作协程数量为CPU核心数的两倍
	if workerCount > 8 {         // 限制最大工作协程数量
		workerCount = 8 // 不超过8个
	}

	// 结果和错误通道
	resultChan := make(chan map[string]float32, workerCount) // 创建一个带缓冲的结果通道
	errChan := make(chan error, workerCount)                 // 创建一个带缓冲的错误通道
	var wg sync.WaitGroup                                    // 创建一个等待组用于同步

	// 将文档分成多个批次
	var batches [][]storage.SearchResult             // 声明一个切片用于存放所有批次
	for i := 0; i < len(documents); i += BatchSize { // 按批次大小遍历文档
		end := i + BatchSize      // 计算批次结束索引
		if end > len(documents) { // 如果结束索引超出总数
			end = len(documents) // 将结束索引设为总数
		}
		batches = append(batches, documents[i:end]) // 将批次添加到切片中
	}

	// 批次任务通道
	batchChan := make(chan []storage.SearchResult, len(batches)) // 创建一个带缓冲的批次任务通道
	for _, batch := range batches {                              // 遍历所有批次
		batchChan <- batch // 将批次发送到通道中
	}
	close(batchChan) // 关闭通道，表示没有更多任务了

	// 启动工作协程
	for i := 0; i < workerCount; i++ { // 启动指定数量的协程
		wg.Add(1)   // 等待组计数加一
		go func() { // 启动一个匿名协程
			defer wg.Done() // 协程结束时通知等待组

			for batch := range batchChan { // 从任务通道接收批次任务
				scores, err := h.processBatch(ctx, query, batch) // 处理单个批次
				if err != nil {                                  // 如果处理出错
					select { // 尝试将错误发送到错误通道
					case errChan <- err: // 发送错误
					default: // 如果通道已满，则不阻塞
					}
					return // 结束当前协程
				}

				if scores != nil { // 如果处理成功并返回了分数
					select { // 尝试将结果发送到结果通道
					case resultChan <- scores: // 发送结果
					case <-ctx.Done(): // 如果上下文被取消
						return // 结束当前协程
					}
				}
			}
		}()
	}

	// 等待所有工作完成
	go func() { // 启动一个协程来等待所有工作协程完成
		wg.Wait()         // 等待所有协程执行完毕
		close(resultChan) // 关闭结果通道
		close(errChan)    // 关闭错误通道
	}()

	// 合并结果
	mergedScores := make(map[string]float32) // 创建一个map用于合并所有批次的结果
	for scores := range resultChan {         // 从结果通道接收分数
		for id, score := range scores { // 遍历单个批次的分数
			mergedScores[id] = score // 合并到最终的map中
		}
	}

	// 检查错误
	select { // 检查错误通道中是否有错误
	case err := <-errChan: // 接收错误
		if err != nil { // 如果有错误
			return nil, err // 返回错误
		}
	default: // 如果没有错误
	}

	h.logger.Printf("批处理Reranker完成，共处理 %d 批次，获得 %d 个重排分数", len(batches), len(mergedScores)) // 记录日志
	return mergedScores, nil                                                                // 返回合并后的分数
}

// processBatch 处理单个批次的Reranker请求
func (h *JobSearchHandler) processBatch(ctx context.Context, query string, batch []storage.SearchResult) (map[string]float32, error) { // 定义处理单个Reranker批次的方法
	// 准备请求文档
	var docsToRerank []RerankDocument // 声明一个切片用于存放待重排的文档

	for _, res := range batch { // 遍历批次中的每个搜索结果
		// 尝试从多个可能的字段获取内容
		var content string                                                  // 声明变量存储文档内容
		var found bool                                                      // 声明标志位表示是否找到内容
		possibleFields := []string{"chunk_content_text", "content", "text"} // 定义可能包含文本内容的字段名

		for _, field := range possibleFields { // 遍历可能的字段名
			if textContent, ok := res.Payload[field].(string); ok && textContent != "" { // 尝试从payload中获取并断言为字符串
				content = textContent // 获取内容
				found = true          // 设置找到标志
				break                 // 跳出循环
			}
		}

		// 如果找不到，尝试任意字符串字段
		if !found { // 如果在预设字段中未找到
			for _, v := range res.Payload { // 遍历payload中的所有值
				if textContent, ok := v.(string); ok && textContent != "" { // 尝试断言为字符串
					content = textContent // 获取内容
					found = true          // 设置找到标志
					break                 // 跳出循环
				}
			}
		}

		if found { // 如果找到了内容
			docsToRerank = append(docsToRerank, RerankDocument{ // 将文档添加到待重排列表
				ID:   res.ID,  // 文档ID
				Text: content, // 文档内容
			})
		}
	}

	if len(docsToRerank) == 0 { // 如果没有需要重排的文档
		return nil, nil // 直接返回nil
	}

	// 构造请求
	reqBody := RerankRequest{ // 创建Reranker请求体
		Query:     query,        // 设置查询
		Documents: docsToRerank, // 设置待重排文档
	}

	reqBytes, err := json.Marshal(reqBody) // 将请求体序列化为JSON
	if err != nil {                        // 如果序列化失败
		return nil, err // 返回错误
	}

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", h.cfg.Reranker.URL, bytes.NewBuffer(reqBytes)) // 创建一个带上下文的HTTP POST请求
	if err != nil {                                                                                    // 如果创建请求失败
		return nil, err // 返回错误
	}
	req.Header.Set("Content-Type", "application/json") // 设置请求头

	// 设置超时
	client := &http.Client{Timeout: 30 * time.Second} // 创建一个带30秒超时的HTTP客户端

	// 发送请求
	resp, err := client.Do(req) // 发送HTTP请求
	if err != nil {             // 如果发送失败
		return nil, err // 返回错误
	}
	defer resp.Body.Close() // 确保响应体被关闭

	// 检查响应状态
	if resp.StatusCode != http.StatusOK { // 如果响应状态码不是200 OK
		bodyBytes, _ := io.ReadAll(resp.Body)                                                // 读取响应体内容
		return nil, fmt.Errorf("Reranker服务返回错误: %d, %s", resp.StatusCode, string(bodyBytes)) // 返回包含状态码和响应体的错误信息
	}

	// 解析响应
	var rerankedDocs []RerankedDocument                                      // 声明一个切片用于存放解析后的重排文档
	if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil { // 解码JSON响应体
		return nil, err // 如果解码失败，返回错误
	}

	// 提取分数
	rerankScores := make(map[string]float32) // 创建一个map用于存储提取的分数
	for _, doc := range rerankedDocs {       // 遍历重排后的文档
		rerankScores[doc.ID] = doc.RerankScore // 将分数存入map
	}

	return rerankScores, nil // 返回分数map和nil错误
}

// HandleCheckSearchStatus 处理检查搜索状态的请求
func (h *JobSearchHandler) HandleCheckSearchStatus(ctx context.Context, c *app.RequestContext) { // 定义检查搜索状态的API处理函数
	jobID := c.Param("job_id")          // 从URL路径参数获取job_id
	hrIDValue, exists := c.Get("hr_id") // 从请求上下文中获取hr_id
	if !exists {                        // 如果hr_id不存在
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取 HR ID 失败"}) // 返回500错误
		return                                                                              // 结束函数执行
	}
	hrID := hrIDValue.(string) // 将hr_id断言为字符串

	searchID := fmt.Sprintf(constants.KeySearchSession, jobID, hrID) // 生成搜索会话ID
	lockKey := fmt.Sprintf(constants.KeySearchLock, jobID, hrID)     // 生成分布式锁键

	// 检查缓存中是否存在结果
	existsCount, err := h.storage.Redis.Client.Exists(ctx, searchID).Result() // 检查Redis中是否存在搜索结果缓存
	if err != nil {                                                           // 如果检查出错
		h.logger.Printf("检查 searchID: %s 失败: %v", searchID, err)                         // 记录错误日志
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "检查搜索状态失败"}) // 返回500错误
		return                                                                           // 结束函数执行
	}

	if existsCount > 0 { // 如果缓存存在
		// 缓存存在，返回已完成状态
		totalCount, err := h.storage.Redis.Client.ZCard(ctx, searchID).Result() // 获取缓存中结果的总数
		if err != nil {                                                         // 如果获取总数出错
			h.logger.Printf("获取缓存元素数量失败: %v", err)                                             // 记录错误日志
			c.JSON(consts.StatusInternalServerError, map[string]string{"error": "获取搜索结果数量失败"}) // 返回500错误
			return                                                                             // 结束函数执行
		}

		c.JSON(consts.StatusOK, map[string]interface{}{ // 返回200成功响应
			"status":      "completed", // 状态：已完成
			"total_count": totalCount,  // 结果总数
		})
		return // 结束函数执行
	}

	// 检查搜索锁是否存在
	lockExistsCount, err := h.storage.Redis.Client.Exists(ctx, lockKey).Result() // 检查分布式锁是否存在
	if err != nil {                                                              // 如果检查出错
		h.logger.Printf("检查搜索锁状态失败: %v", err)                                            // 记录错误日志
		c.JSON(consts.StatusInternalServerError, map[string]string{"error": "检查搜索状态失败"}) // 返回500错误
		return                                                                           // 结束函数执行
	}

	if lockExistsCount > 0 { // 如果锁存在
		// 锁存在，表示搜索正在进行
		c.JSON(consts.StatusOK, map[string]interface{}{ // 返回200成功响应
			"status":      "processing", // 状态：处理中
			"message":     "搜索正在处理中",    // 消息
			"retry_after": 2,            // 建议客户端2秒后重试
		})
		return // 结束函数执行
	}

	// 既无锁也无缓存，表示尚未开始搜索或者搜索已过期
	c.JSON(consts.StatusOK, map[string]interface{}{ // 返回200成功响应
		"status":  "not_found",     // 状态：未找到
		"message": "未找到搜索结果或结果已过期", // 消息
	})
}
