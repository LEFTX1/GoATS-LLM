package handler_test

import (
	"ai-agent-go/internal/api/handler"
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/constants"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/processor"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route/param"
	"github.com/stretchr/testify/require"
)

const (
	paginatedTestConfigPath = "../../config/config.yaml"
)

// TestResumePaginatedHandler 测试简历分页查询处理器
// 这是一个集成测试，它首先调用工作搜索处理器来生成缓存，然后测试分页处理器是否能正确地从缓存中读取数据。
func TestResumePaginatedHandler(t *testing.T) {
	// 1. 设置
	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此集成测试")
	}

	ctx := context.Background()

	cfg, err := config.LoadConfigFromFileAndEnv(paginatedTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	require.NotNil(t, s.Redis, "必须配置Redis")
	require.NotNil(t, s.MySQL, "必须配置MySQL")
	require.NotNil(t, s.Qdrant, "必须配置Qdrant")

	// 2. 定义测试数据
	testJobID := "job-pagination-test-002"
	testHRID := "hr-pagination-test-002"
	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。

**主要职责:**
1.  负责核心业务系统的后端服务设计与开发，使用Go语言（Golang）作为主要开发语言。
2.  参与微服务架构的演进，使用gRPC进行服务间通信，并基于Protobuf进行接口定义。
3.  构建和维护高并发、高可用的系统，有处理秒杀、实时消息等大流量场景的经验。
4.  深入使用和优化缓存（Redis）和消息队列（Kafka/RabbitMQ），实现系统解耦和性能提升。
5.  将服务容器化（Docker）并部署在Kubernetes（K8s）集群上，熟悉云原生生态。
6.  关注系统性能，能够使用pprof等工具进行性能分析和调优。

**任职要求:**
1.  计算机相关专业本科及以上学历，3年以上Go语言后端开发经验。
2.  精通Go语言及其并发模型（Goroutine, Channel, Context）。
3.  熟悉至少一种主流Go微服务框架，如Go-Zero, Gin, Kratos等。
4.  熟悉gRPC, Protobuf，有丰富的微服务API设计经验。
5.  熟悉MySQL, Redis, Kafka等常用组件，并有生产环境应用经验。
6.  熟悉Docker和Kubernetes，理解云原生的基本理念。

**加分项:**
1.  有主导大型微服务项目重构或设计的经验。
2.  熟悉Service Mesh（如Istio）、分布式追踪（OpenTelemetry）等服务治理技术。
3.  对分布式存储（如TiKV）、共识算法（Raft）有深入研究或实践。
4.  有开源项目贡献或活跃的技术博客。`

	// 3. 准备数据库和缓存
	job := models.Job{
		JobID:              testJobID,
		JobDescriptionText: jobDesc,
	}
	// 使用Create，如果记录已存在会失败，这有助于确保测试的隔离性
	err = s.MySQL.DB().WithContext(ctx).Create(&job).Error
	if err != nil {
		// 如果记录已存在，为了使测试可重复运行，我们先删除它再创建
		s.MySQL.DB().WithContext(ctx).Where("job_id = ?", testJobID).Delete(&models.Job{})
		err = s.MySQL.DB().WithContext(ctx).Create(&job).Error
	}
	require.NoError(t, err, "在数据库中创建测试工作失败")

	// 清理逻辑
	t.Cleanup(func() {
		s.MySQL.DB().WithContext(ctx).Delete(&job)
		cacheKey := fmt.Sprintf(constants.KeySearchSession, testJobID, testHRID)
		lockKey := fmt.Sprintf(constants.KeySearchLock, testJobID, testHRID)
		jdCacheKey := fmt.Sprintf(constants.KeyJobDescriptionText, testJobID)
		s.Redis.Client.Del(ctx, cacheKey, lockKey, jdCacheKey)
		t.Log("已从数据库和Redis清理测试数据")
	})

	// 4. 初始化处理器
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")
	jdProcessor, err := processor.NewJDProcessor(embedder, s, cfg.Aliyun.Embedding.Model)
	require.NoError(t, err, "初始化JD处理器失败")
	jobSearchHandler := handler.NewJobSearchHandler(cfg, s, jdProcessor)
	resumePaginatedHandler := handler.NewResumePaginatedHandler(cfg, s)

	// 5. 阶段1: 运行搜索以填充缓存
	t.Logf("--- 阶段1: 为JobID=%s运行职位搜索以填充缓存 ---", testJobID)

	// 确保没有旧的锁或缓存
	lockKey := fmt.Sprintf(constants.KeySearchLock, testJobID, testHRID)
	s.Redis.Client.Del(ctx, lockKey)
	cacheKey := fmt.Sprintf(constants.KeySearchSession, testJobID, testHRID)
	s.Redis.Client.Del(ctx, cacheKey)

	cSearch := app.NewContext(16)
	cSearch.Params = append(cSearch.Params, param.Param{Key: "job_id", Value: testJobID})
	cSearch.Set("hr_id", testHRID)
	cSearch.QueryArgs().Add("display_limit", "1")

	jobSearchHandler.HandleSearchResumesByJobID(ctx, cSearch)

	require.Equal(t, consts.StatusOK, cSearch.Response.StatusCode(), "职位搜索处理器运行失败以填充缓存。响应体: %s", string(cSearch.Response.Body()))

	// 验证缓存是否已创建
	cacheExists, err := s.Redis.Client.Exists(ctx, cacheKey).Result()
	require.NoError(t, err)
	require.Equal(t, int64(1), cacheExists, "搜索处理器未创建缓存条目")
	t.Log("--- 缓存已成功填充 ---")

	// 6. 阶段2: 测试分页
	t.Logf("--- 阶段2: 为JobID=%s测试分页 ---", testJobID)
	_, currentFilePath, _, ok := runtime.Caller(0)
	require.True(t, ok, "获取当前文件路径失败")
	currentDir := filepath.Dir(currentFilePath)

	type PaginationResults struct {
		TotalCount int64                           `json:"total_count"`
		Pages      []types.PaginatedResumeResponse `json:"pages"`
	}

	results := PaginationResults{
		Pages: make([]types.PaginatedResumeResponse, 0, 3),
	}

	var currentCursor int64 = 0
	displayLimit := 10

	// 查询最多3页
	for pageNum := 1; pageNum <= 3; pageNum++ {
		t.Logf("查询第%d页数据...", pageNum)
		cPaginated := app.NewContext(16)
		cPaginated.Params = append(cPaginated.Params, param.Param{Key: "job_id", Value: testJobID})
		cPaginated.Set("hr_id", testHRID)
		cPaginated.QueryArgs().Add("cursor", fmt.Sprintf("%d", currentCursor))
		cPaginated.QueryArgs().Add("size", fmt.Sprintf("%d", displayLimit))

		resumePaginatedHandler.HandlePaginatedResumeSearch(ctx, cPaginated)

		require.Equal(t, consts.StatusOK, cPaginated.Response.StatusCode(), "分页处理器在第%d页失败", pageNum)

		var respData types.PaginatedResumeResponse
		err = json.Unmarshal(cPaginated.Response.Body(), &respData)
		require.NoError(t, err, "解析第%d页响应失败", pageNum)

		// 保存页面原始JSON响应
		pagePath := filepath.Join(currentDir, fmt.Sprintf("paginated_resume_page%d.json", pageNum))
		pageJSON, err := json.MarshalIndent(respData, "", "  ")
		require.NoError(t, err)
		err = os.WriteFile(pagePath, pageJSON, 0644)
		require.NoError(t, err)
		t.Logf("第%d页原始JSON结果已保存到: %s", pageNum, pagePath)

		results.Pages = append(results.Pages, respData)
		if pageNum == 1 {
			results.TotalCount = respData.TotalCount
		}

		t.Logf("第%d页查询结果: 总数=%d, 返回=%d份简历, 下一页游标=%d",
			pageNum, respData.TotalCount, len(respData.Resumes), respData.NextCursor)

		for i, resume := range respData.Resumes {
			t.Logf("  第%d页简历 #%d: UUID=%s, 块数=%d",
				pageNum, i+1, resume.SubmissionUUID, len(resume.Chunks))
		}

		// 如果没有更多结果或到达最后一页，则中断
		if respData.NextCursor == currentCursor || len(respData.Resumes) < displayLimit {
			t.Logf("在第%d页到达结果的最后一页。", pageNum)
			break
		}

		currentCursor = respData.NextCursor
		// 增加短暂的延迟以避免在非常快的测试执行中出现竞争条件
		time.Sleep(100 * time.Millisecond)
	}

	// 将合并结果保存到JSON文件
	resultsJSON, err := json.MarshalIndent(results, "", "  ")
	require.NoError(t, err, "序列化合并结果为JSON失败")

	combinedPath := filepath.Join(currentDir, "paginated_resume_combined.json")
	err = os.WriteFile(combinedPath, resultsJSON, 0644)
	require.NoError(t, err, "保存合并结果到JSON文件失败")
	t.Logf("已将%d页的合并结果保存到: %s", len(results.Pages), combinedPath)
}
