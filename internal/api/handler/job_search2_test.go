package handler_test

import (
	"ai-agent-go/internal/config"
	appCoreLogger "ai-agent-go/internal/logger"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/storage"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	glog "github.com/cloudwego/hertz/pkg/common/hlog"
	hertzadapter "github.com/hertz-contrib/logger/zerolog"
	"github.com/stretchr/testify/require"
)

// RerankDocument 定义了发送给Reranker服务的文档结构
type RerankDocument struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// RerankRequest 定义了发送给Reranker服务的请求体结构
type RerankRequest struct {
	Query     string           `json:"query"`
	Documents []RerankDocument `json:"documents"`
}

// RerankedDocument 定义了从Reranker服务接收的文档结构
type RerankedDocument struct {
	ID          string  `json:"id"`
	RerankScore float32 `json:"rerank_score"`
}

// GoldenTruthSet 定义了用于评测的黄金标准答案
type GoldenTruthSet struct {
	Tier1 map[string]bool // 高相关
	Tier2 map[string]bool // 中相关
	Tier3 map[string]bool // 低相关
}

// getGoBackendGoldenTruthSet 返回Go后端工程师岗位的黄金标准评测集
func getGoBackendGoldenTruthSet() GoldenTruthSet {
	// Tier 1: 高相关 (30份)
	tier1 := map[string]bool{
		"0197m3h2-d001-e001-f001-000000000001": true, "0197m3h2-d001-e001-f001-000000000002": true,
		"0197m3h2-d001-e001-f001-000000000003": true, "0197m3h2-d001-e001-f001-000000000004": true,
		"0197m3h2-d001-e001-f001-000000000005": true, "0197m3h2-d001-e001-f001-000000000006": true,
		"0197m3h2-d001-e001-f001-000000000007": true, "0197m3h2-d001-e001-f001-000000000008": true,
		"0197m3h2-d001-e001-f001-000000000009": true, "0197m3h2-d001-e001-f001-000000000010": true,
		"0197m3h2-d001-e001-f001-000000000011": true, "0197m3h2-d001-e001-f001-000000000012": true,
		"0197m3h2-d001-e001-f001-000000000013": true, "0197m3h2-d001-e001-f001-000000000014": true,
		"0197m3h2-d001-e001-f001-000000000015": true, "0197m3h2-d001-e001-f001-000000000016": true,
		"0197m3h2-d001-e001-f001-000000000017": true, "0197m3h2-d001-e001-f001-000000000018": true,
		"0197m3h2-d001-e001-f001-000000000019": true, "0197m3h2-d001-e001-f001-000000000020": true,
		"0197m3h2-d001-e001-f001-000000000021": true, "0197m3h2-d001-e001-f001-000000000022": true,
		"0197m3h2-d001-e001-f001-000000000023": true, "0197m3h2-d001-e001-f001-000000000024": true,
		"0197l2g2-d001-e001-f001-000000000001": true, "0197l2g2-d001-e001-f001-000000000002": true,
		"0197l2g2-d001-e001-f001-000000000003": true, "0197l2g2-d001-e001-f001-000000000004": true,
		"0197l2g2-d001-e001-f001-000000000005": true, "0197l2g2-d001-e001-f001-000000000006": true,
	}

	// Tier 2: 中相关 (24份)
	tier2 := map[string]bool{
		"0197m3h2-d001-e001-f001-000000000025": true, "0197m3h2-d001-e001-f001-000000000026": true,
		"0197m3h2-d001-e001-f001-000000000027": true, "0197m3h2-d001-e001-f001-000000000028": true,
		"0197m3h2-d001-e001-f001-000000000029": true, "0197m3h2-d001-e001-f001-000000000030": true,
		"0197m3h2-d001-e001-f001-000000000031": true, "0197m3h2-d001-e001-f001-000000000032": true,
		"0197m3h2-d001-e001-f001-000000000033": true, "0197m3h2-d001-e001-f001-000000000034": true,
		"0197m3h2-d001-e001-f001-000000000035": true, "0197m3h2-d001-e001-f001-000000000036": true,
		"0197m3h2-d001-e001-f001-000000000037": true, "0197m3h2-d001-e001-f001-000000000038": true,
		"0197m3h2-d001-e001-f001-000000000039": true, "0197m3h2-d001-e001-f001-000000000040": true,
		"0197m3h2-d001-e001-f001-000000000041": true, "0197m3h2-d001-e001-f001-000000000042": true,
		"0197m3h2-d001-e001-f001-000000000043": true, "0197m3h2-d001-e001-f001-000000000044": true,
		"0197m3h2-d001-e001-f001-000000000045": true, "0197m3h2-d001-e001-f001-000000000046": true,
		"0197m3h2-d001-e001-f001-000000000047": true, "0197m3h2-d001-e001-f001-000000000048": true,
	}

	// Tier 3: 低相关 (26份)
	tier3 := map[string]bool{
		"0197m3h2-d001-e001-f001-000000000049": true, "0197m3h2-d001-e001-f001-000000000050": true,
		"0197m3h2-d001-e001-f001-000000000051": true, "0197m3h2-d001-e001-f001-000000000052": true,
		"0197m3h2-d001-e001-f001-000000000053": true, "0197m3h2-d001-e001-f001-000000000054": true,
		"0197m3h2-d001-e001-f001-000000000055": true, "0197m3h2-d001-e001-f001-000000000056": true,
		"0197m3h2-d001-e001-f001-000000000057": true, "0197m3h2-d001-e001-f001-000000000058": true,
		"0197m3h2-d001-e001-f001-000000000059": true, "0197m3h2-d001-e001-f001-000000000060": true,
		"0197m3h2-d001-e001-f001-000000000061": true, "0197m3h2-d001-e001-f001-000000000062": true,
		"0197m3h2-d001-e001-f001-000000000063": true, "0197m3h2-d001-e001-f001-000000000064": true,
		"0197l2g2-d001-e001-f001-000000000007": true, "0197l2g2-d001-e001-f001-000000000008": true,
		"0197l2g2-d001-e001-f001-000000000009": true, "0197l2g2-d001-e001-f001-000000000010": true,
		"0197l2g2-d001-e001-f001-000000000011": true, "0197l2g2-d001-e001-f001-000000000012": true,
		"0197l2g2-d001-e001-f001-000000000013": true, "0197l2g2-d001-e001-f001-000000000014": true,
		"0197l2g2-d001-e001-f001-000000000015": true, "0197l2g2-d001-e001-f001-000000000016": true,
	}

	return GoldenTruthSet{
		Tier1: tier1,
		Tier2: tier2,
		Tier3: tier3,
	}
}

// TestGoBackendEngineerSearch 测试Go后端工程师岗位描述向量搜索
func TestGoBackendEngineerSearch(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "GoBackendEngineerSearch")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Go后端工程师岗位描述向量搜索")

	// 1. 设置测试环境
	ctx := context.Background()

	// 初始化日志
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty", TimeFormat: "15:04:05", ReportCaller: true})
	hertzCompatibleLogger := hertzadapter.From(appCoreLogger.Logger)
	glog.SetLogger(hertzCompatibleLogger)
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	testLogger.Log("加载配置文件: %s", jobSearchTestConfigPath)
	cfg, err := config.LoadConfigFromFileOnly(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")
	testLogger.Log("配置加载成功")

	// 2. 初始化存储组件
	testLogger.Log("初始化存储组件")
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()
	testLogger.Log("存储组件初始化成功")

	// 跳过CI环境测试
	if os.Getenv("CI") != "" {
		testLogger.Log("在CI环境中跳过此测试")
		t.Skip("在CI环境中跳过此测试")
	}

	// 检查必要组件是否可用
	if s.Qdrant == nil {
		testLogger.Log("Qdrant未配置或不可用，跳过测试")
		t.Skip("Qdrant未配置或不可用，跳过测试")
	}

	// 确保API密钥配置正确
	if cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		testLogger.Log("未配置有效的阿里云API密钥，跳过测试")
		t.Skip("未配置有效的阿里云API密钥，跳过测试")
	}

	// 初始化embedder
	testLogger.Log("初始化Embedder")
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	require.NoError(t, err, "初始化embedder失败")
	testLogger.Log("Embedder初始化成功，Model: %s, Dimensions: %d", aliyunEmbeddingConfig.Model, aliyunEmbeddingConfig.Dimensions)

	// 定义Go后端工程师岗位描述
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

	testLogger.Log("使用Go后端工程师岗位描述进行搜索")
	testLogger.Log("岗位描述长度: %d", len(jobDesc))

	// 检查Qdrant中的数据量
	count, countErr := s.Qdrant.CountPoints(ctx)
	if countErr != nil {
		testLogger.Log("获取Qdrant点数量失败: %v", countErr)
	} else {
		testLogger.Log("Qdrant集合中总共有 %d 个点", count)
	}

	// 将岗位描述转换为向量
	testLogger.Log("生成岗位描述向量")
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	require.NotEmpty(t, vectors, "生成的向量不能为空")
	require.NotEmpty(t, vectors[0], "生成的向量数组不能为空")

	vector := vectors[0]
	testLogger.Log("成功生成向量，维度: %d", len(vector))

	// 记录向量的前10个元素作为示例
	if len(vector) > 10 {
		sampleVector := vector[:10]
		testLogger.LogObject("向量样本(前10个元素)", sampleVector)
	}

	// 在Qdrant中搜索相似简历，设置limit为300
	limit := 300
	testLogger.Log("在Qdrant中搜索，限制结果数: %d", limit)
	results, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil)
	require.NoError(t, err, "向量搜索失败")

	// 分析搜索结果
	testLogger.Log("搜索返回了 %d 个结果", len(results))
	t.Logf("搜索返回了 %d 个结果", len(results))

	if len(results) == 0 {
		// 如果没有结果，检查Qdrant集合中的点数量
		testLogger.Log("没有找到匹配的简历")
		t.Log("没有找到匹配的简历")
	} else {
		// 在分析搜索结果的代码块中，结果处理逻辑之前添加
		if len(results) > 0 {
			// 收集所有的UUID
			var allUUIDs []string
			for _, result := range results {
				if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
					allUUIDs = append(allUUIDs, uuid)
				}
			}

			// 输出所有UUID到日志（以逗号分隔）
			testLogger.Log("=== 所有简历UUID列表 ===")
			testLogger.Log("UUID 总数: %d", len(allUUIDs))
			testLogger.Log("UUID 列表(逗号分隔): %s", strings.Join(allUUIDs, ","))

			// 输出所有UUID到日志（每行一个，便于复制）
			testLogger.Log("=== 所有简历UUID（每行一个） ===")
			for _, uuid := range allUUIDs {
				testLogger.Log("%s", uuid)
			}
		}

		// 如果有结果，开始详细分析
		testLogger.Log("=== 开始分析搜索结果 ===")

		// 记录各个分数区间的结果数量
		scoreRanges := map[string]int{
			"极高匹配 (0.9-1.0)": 0,
			"高匹配 (0.8-0.9)":  0,
			"良好匹配 (0.7-0.8)": 0,
			"中等匹配 (0.6-0.7)": 0,
			"一般匹配 (0.5-0.6)": 0,
			"低匹配 (< 0.5)":    0,
		}

		// 记录技能关键词出现的次数
		skillKeywords := map[string]int{
			"go":            0,
			"golang":        0,
			"微服务":           0,
			"grpc":          0,
			"protobuf":      0,
			"redis":         0,
			"kafka":         0,
			"rabbitmq":      0,
			"docker":        0,
			"kubernetes":    0,
			"k8s":           0,
			"gin":           0,
			"kratos":        0,
			"go-zero":       0,
			"mysql":         0,
			"高并发":           0,
			"分布式":           0,
			"pprof":         0,
			"性能优化":          0,
			"istio":         0,
			"service mesh":  0,
			"opentelemetry": 0,
			"tikv":          0,
			"raft":          0,
		}

		// 对结果进行详细分析
		for i, result := range results {
			// 从payload中获取submission_uuid和其他元数据
			submissionUUID := ""
			if subUUID, ok := result.Payload["submission_uuid"].(string); ok {
				submissionUUID = subUUID
			}

			// 记录该结果
			testLogger.Log("结果 #%d: SubmissionUUID=%s, Score=%.4f", i+1, submissionUUID, result.Score)

			// 根据分数区间进行统计
			switch {
			case result.Score >= 0.9:
				scoreRanges["极高匹配 (0.9-1.0)"]++
			case result.Score >= 0.8:
				scoreRanges["高匹配 (0.8-0.9)"]++
			case result.Score >= 0.7:
				scoreRanges["良好匹配 (0.7-0.8)"]++
			case result.Score >= 0.6:
				scoreRanges["中等匹配 (0.6-0.7)"]++
			case result.Score >= 0.5:
				scoreRanges["一般匹配 (0.5-0.6)"]++
			default:
				scoreRanges["低匹配 (< 0.5)"]++
			}

			// 获取简历内容进行详细分析
			if s.MySQL != nil && i < 50 { // 只详细分析前50个结果，避免日志过大
				var chunkContent string
				err := s.MySQL.DB().WithContext(ctx).
					Raw("SELECT chunk_content_text FROM resume_submission_chunks WHERE point_id = ?",
						result.ID).Scan(&chunkContent).Error

				if err == nil {
					// 记录内容到日志
					testLogger.Log("结果 #%d 内容:", i+1)
					testLogger.Log("%s", chunkContent)

					// 统计关键词出现情况
					chunkContentLower := strings.ToLower(chunkContent)
					for keyword := range skillKeywords {
						if strings.Contains(chunkContentLower, strings.ToLower(keyword)) {
							skillKeywords[keyword]++
						}
					}

					// 尝试评估简历质量
					quality := assessResumeQuality(chunkContentLower, skillKeywords)
					testLogger.Log("结果 #%d 质量评估: %s", i+1, quality)
				}
			}
		}

		// 输出分数区间统计
		testLogger.Log("=== 分数区间统计 ===")
		for rang, count := range scoreRanges {
			testLogger.Log("%s: %d个简历 (%.1f%%)", rang, count, float64(count)/float64(len(results))*100)
		}

		// 输出技能关键词统计
		testLogger.Log("=== 技能关键词出现次数统计 (前50个结果) ===")
		// 将关键词按出现次数排序
		type keywordCount struct {
			Keyword string
			Count   int
		}
		var sortedKeywords []keywordCount
		for k, v := range skillKeywords {
			if v > 0 { // 只统计出现过的关键词
				sortedKeywords = append(sortedKeywords, keywordCount{k, v})
			}
		}
		sort.Slice(sortedKeywords, func(i, j int) bool {
			return sortedKeywords[i].Count > sortedKeywords[j].Count
		})
		for _, kc := range sortedKeywords {
			testLogger.Log("%s: 出现在%d个简历块中", kc.Keyword, kc.Count)
		}
	}

	testLogger.Log("测试完成")
}

// TestGoBackendEngineerSearchWithGoldenTruthSet 测试Go后端工程师岗位并使用黄金标准进行评测
func TestGoBackendEngineerSearchWithGoldenTruthSet(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "GoBackendEngineerSearchWithGoldenTruthSet")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Go后端工程师岗位描述向量搜索（带黄金标准评测）")

	// 1. 设置测试环境
	ctx := context.Background()
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty"})
	glog.SetLogger(hertzadapter.From(appCoreLogger.Logger))
	glog.SetLevel(glog.LevelDebug)

	// 加载配置
	cfg, err := config.LoadConfigFromFileOnly(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	// 2. 初始化存储组件
	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	// 跳过CI环境
	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}
	if s.Qdrant == nil || cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		t.Skip("跳过测试：Qdrant或阿里云API Key未配置")
	}

	// 初始化Embedder
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

	// 定义Go后端工程师岗位描述
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

	// 生成JD向量
	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	vector := vectors[0]

	// 在Qdrant中搜索
	limit := 300
	initialResults, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil)
	require.NoError(t, err, "向量搜索失败")
	testLogger.Log("阶段1: 向量搜索完成。从Qdrant召回 %d 个初步结果。", len(initialResults))

	// 阶段2: 海选过滤 (Score Threshold)
	scoreThreshold := float32(0.50)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("阶段2: 海选过滤完成。应用score_threshold=%.2f, 剩下 %d 个候选结果进入重排。", scoreThreshold, len(candidatesForRerank))

	// 阶段3: 调用Reranker服务进行精排
	var finalResults []storage.SearchResult
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, candidatesForRerank, t, testLogger)
	if err != nil {
		testLogger.Log("警告: Reranker调用失败: %v。将回退到使用原始向量搜索分数进行评测。", err)
		finalResults = initialResults // 回退到原始结果
	} else if rerankScores == nil {
		testLogger.Log("警告: Reranker未启用或未返回结果。将回退到使用原始向量搜索分数进行评测。")
		finalResults = initialResults // 回退到原始结果
	} else {
		testLogger.Log("阶段3: Reranker精排完成。")
		// 使用rerank分数更新结果并过滤
		var rerankedResults []storage.SearchResult
		for _, res := range candidatesForRerank {
			if newScore, ok := rerankScores[res.ID]; ok {
				res.Score = newScore // 使用rerank分数覆盖原始分数
				rerankedResults = append(rerankedResults, res)
			}
		}

		// 根据新的rerank分数进行排序（降序）
		sort.Slice(rerankedResults, func(i, j int) bool {
			return rerankedResults[i].Score > rerankedResults[j].Score
		})
		finalResults = rerankedResults
		testLogger.Log("使用 %d 个重排后的结果进行最终评测。", len(finalResults))
	}

	// 使用最终结果进行评测
	results := finalResults
	t.Logf("用于最终评测的结果数量: %d", len(results))

	// 获取黄金标准
	goldenTruth := getGoBackendGoldenTruthSet()

	// 分析结果
	foundTier1 := make(map[string]float32)
	foundTier2 := make(map[string]float32)
	foundTier3 := make(map[string]float32)
	unknownFound := make(map[string]float32)
	allFoundUUIDs := make(map[string]bool)

	for _, result := range results {
		if uuid, ok := result.Payload["submission_uuid"].(string); ok && uuid != "" {
			// 如果已经处理过该简历的更高分数的块，则跳过
			if allFoundUUIDs[uuid] {
				continue
			}

			if goldenTruth.Tier1[uuid] {
				foundTier1[uuid] = result.Score
			} else if goldenTruth.Tier2[uuid] {
				foundTier2[uuid] = result.Score
			} else if goldenTruth.Tier3[uuid] {
				foundTier3[uuid] = result.Score
			} else {
				unknownFound[uuid] = result.Score
			}
			allFoundUUIDs[uuid] = true // 标记该简历已处理
		}
	}

	// 输出评测报告
	reportTitle := "Go后端岗位评测报告"
	if rerankScores != nil && err == nil {
		reportTitle += " (Reranked)"
	} else {
		reportTitle += " (Vector Search Only)"
	}
	testLogger.Log("=== %s (Top %d) ===", reportTitle, len(results))
	testLogger.Log("==========================================")

	// Tier 1 评测
	tier1Recall := float64(len(foundTier1)) / float64(len(goldenTruth.Tier1)) * 100
	testLogger.Log("高相关 (Tier 1):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier1))
	testLogger.Log("  - 召回数量: %d", len(foundTier1))
	testLogger.Log("  - 召回率: %.2f%%", tier1Recall)

	// Tier 2 评测
	tier2Recall := float64(len(foundTier2)) / float64(len(goldenTruth.Tier2)) * 100
	testLogger.Log("中相关 (Tier 2):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier2))
	testLogger.Log("  - 召回数量: %d", len(foundTier2))
	testLogger.Log("  - 召回率: %.2f%%", tier2Recall)

	// Tier 3 评测 (噪音)
	tier3Noise := float64(len(foundTier3)) / float64(len(goldenTruth.Tier3)) * 100
	testLogger.Log("低相关/噪音 (Tier 3):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier3))
	testLogger.Log("  - 召回数量: %d", len(foundTier3))
	testLogger.Log("  - 噪音占比: %.2f%% (越低越好)", tier3Noise)

	// 其他项
	testLogger.Log("未分类项 (Unknown):")
	testLogger.Log("  - 召回数量: %d", len(unknownFound))

	testLogger.Log("==========================================")

	// --- 详细分数分布分析 ---
	testLogger.Log("\n--- 详细分数分布分析 ---")

	// 辅助函数，用于分析和记录每个层级的分数详情
	logScoreDetails(testLogger, "高相关 (Tier 1)", foundTier1)
	logScoreDetails(testLogger, "中相关 (Tier 2)", foundTier2)
	logScoreDetails(testLogger, "低相关/噪音 (Tier 3)", foundTier3)
	logScoreDetails(testLogger, "未分类项 (Unknown)", unknownFound)

	testLogger.Log("==========================================")
	testLogger.Log("测试完成")
}

// TestRerankerService 是一个独立的单元测试，用于检查Reranker服务的可用性和基本逻辑
func TestRerankerService(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "RerankerServiceConnectivity")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Reranker服务连通性...")

	// 1. 加载配置以获取Reranker URL
	ctx := context.Background()
	cfg, err := config.LoadConfigFromFileOnly(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")
	require.NotNil(t, cfg, "配置不能为空")

	// 如果Reranker未启用或URL为空，则跳过测试
	if !cfg.Reranker.Enabled || cfg.Reranker.URL == "" {
		t.Skip("Reranker服务未启用或URL未配置，跳过此测试")
	}
	testLogger.Log("Reranker服务已启用，URL: %s", cfg.Reranker.URL)

	// 2. 构造模拟数据
	query := "高级Go语言工程师，需要微服务和gRPC经验"
	mockResults := []storage.SearchResult{
		{
			ID:    "doc-1-go-expert",
			Score: 0.8, // 原始向量分数
			Payload: map[string]interface{}{
				"content": "这位候选人精通Go语言，有丰富的gRPC和Protobuf实践，并主导过大型微服务项目。",
			},
		},
		{
			ID:    "doc-2-python-dev",
			Score: 0.7, // 原始向量分数
			Payload: map[string]interface{}{
				"content": "该开发者主要使用Python和Django进行Web后端开发，了解过Go但没有项目经验。",
			},
		},
		{
			ID:    "doc-3-frontend",
			Score: 0.6, // 原始向量分数
			Payload: map[string]interface{}{
				"content": "专注于前端开发，使用React和Vue框架，后端技能不匹配。",
			},
		},
	}
	testLogger.Log("构造了 %d 条模拟数据用于测试", len(mockResults))

	// 3. 调用Reranker服务
	testLogger.Log("调用Reranker服务...")
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, query, mockResults, t, testLogger)

	// 4. 验证结果
	require.NoError(t, err, "调用Reranker服务不应返回错误")
	require.NotNil(t, rerankScores, "Reranker返回的分数不应为nil")
	require.Equal(t, len(mockResults), len(rerankScores), "返回的分数数量应与发送的文档数量相等")
	testLogger.Log("Reranker服务成功返回了 %d 个分数", len(rerankScores))

	// 5. 打印并检查分数
	testLogger.Log("--- Rerank分数详情 ---")
	for _, doc := range mockResults {
		score, ok := rerankScores[doc.ID]
		require.True(t, ok, "每个文档都应该有一个rerank分数, doc ID: %s", doc.ID)
		testLogger.Log("文档ID: %s, Rerank Score: %.6f", doc.ID, score)
	}

	// 简单的逻辑断言：预期相关文档的分数更高
	scoreGoExpert := rerankScores["doc-1-go-expert"]
	scorePythonDev := rerankScores["doc-2-python-dev"]
	scoreFrontend := rerankScores["doc-3-frontend"]
	require.True(t, scoreGoExpert > scorePythonDev, "预期Go专家的分数(%.4f)应高于Python开发(%.4f)", scoreGoExpert, scorePythonDev)
	require.True(t, scorePythonDev > scoreFrontend, "预期Python开发的分数(%.4f)应高于前端开发(%.4f)", scorePythonDev, scoreFrontend)

	testLogger.Log("分数逻辑符合预期！")
	testLogger.Log("Reranker服务连通性测试通过！")
}

// callReranker 是一个辅助函数，用于调用外部的Reranker服务
func callReranker(ctx context.Context, rerankerURL string, query string, documents []storage.SearchResult, t *testing.T, logger *TestLogger) (map[string]float32, error) {
	if rerankerURL == "" {
		logger.Log("Reranker URL未配置，跳过重排序")
		return nil, nil
	}

	var docsToRerank []RerankDocument
	for _, res := range documents {
		// 从payload中安全地提取所有需要的字段
		content, ok := res.Payload["content"].(string)
		if !ok || content == "" {
			logger.Log("警告: point_id %s 的payload中'content'字段为空或不存在，跳过此文档", res.ID)
			continue
		}
		chunkType, _ := res.Payload["chunk_type"].(string)
		chunkTitle, _ := res.Payload["chunk_title"].(string)

		// 构建带有上下文的输入文本
		var builder strings.Builder
		if chunkType != "" {
			builder.WriteString(fmt.Sprintf("[Chunk Type: %s]\n", chunkType))
		}
		if chunkTitle != "" {
			builder.WriteString(fmt.Sprintf("[Chunk Title: %s]\n", chunkTitle))
		}
		builder.WriteString("\n")
		builder.WriteString(content)

		fullText := builder.String()

		docsToRerank = append(docsToRerank, RerankDocument{
			ID:   res.ID,
			Text: fullText,
		})
	}

	if len(docsToRerank) == 0 {
		logger.Log("没有可用于重排的文档。")
		return nil, nil
	}

	reqBody := RerankRequest{
		Query:     query,
		Documents: docsToRerank,
	}

	reqBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化rerank请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rerankerURL, bytes.NewBuffer(reqBytes))
	if err != nil {
		return nil, fmt.Errorf("创建rerank请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second} // 增加超时时间
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用rerank服务失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("rerank服务返回错误状态 %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var rerankedDocs []RerankedDocument
	if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil {
		return nil, fmt.Errorf("解析rerank响应失败: %w", err)
	}

	logger.Log("Reranker服务成功返回 %d 个重排序结果", len(rerankedDocs))

	rerankScores := make(map[string]float32)
	for _, doc := range rerankedDocs {
		rerankScores[doc.ID] = doc.RerankScore
	}

	return rerankScores, nil
}

// TestGoBackendEngineerSearch_WithScoreAggregation 测试带有分数聚合策略的Go后端工程师岗位搜索
func TestGoBackendEngineerSearch_WithScoreAggregation(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "GoBackendEngineerSearch_WithScoreAggregation")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试Go后端工程师岗位描述向量搜索（带Top-K分数聚合策略）")

	// 1. 设置测试环境 (与原测试相同)
	ctx := context.Background()
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty"})
	glog.SetLogger(hertzadapter.From(appCoreLogger.Logger))
	glog.SetLevel(glog.LevelDebug)

	cfg, err := config.LoadConfigFromFileOnly(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}
	if s.Qdrant == nil || cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" {
		t.Skip("跳过测试：Qdrant或阿里云API Key未配置")
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

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

	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	vector := vectors[0]

	// 2. 向量搜索与海选 (与原测试相同)
	limit := 300
	initialResults, err := s.Qdrant.SearchSimilarResumes(ctx, vector, limit, nil)
	require.NoError(t, err, "向量搜索失败")
	testLogger.Log("阶段1: 向量搜索完成。从Qdrant召回 %d 个初步结果。", len(initialResults))

	scoreThreshold := float32(0.50)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("阶段2: 海选过滤完成。应用score_threshold=%.2f, 剩下 %d 个候选结果进入重排。", scoreThreshold, len(candidatesForRerank))

	// 3. Reranker精排 (与原测试相同)
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, candidatesForRerank, t, testLogger)
	if err != nil || rerankScores == nil {
		t.Fatalf("Reranker调用失败或未返回结果，测试无法继续: %v", err)
	}
	testLogger.Log("阶段3: Reranker精排完成。")

	// 记录每个返回的rerank score
	testLogger.Log("--- Reranker返回的分数详情 ---")
	type rerankedResultForLog struct {
		PointID        string
		SubmissionUUID string
		Score          float32
	}
	var loggedResults []rerankedResultForLog
	for _, res := range candidatesForRerank {
		if score, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			loggedResults = append(loggedResults, rerankedResultForLog{
				PointID:        res.ID,
				SubmissionUUID: uuid,
				Score:          score,
			})
		}
	}
	// 按rerank分数降序排序
	sort.Slice(loggedResults, func(i, j int) bool {
		return loggedResults[i].Score > loggedResults[j].Score
	})
	// 记录排序后的结果
	for _, res := range loggedResults {
		testLogger.Log("  - PointID: %s, SubmissionUUID: %s, Rerank Score: %.4f", res.PointID, res.SubmissionUUID, res.Score)
	}
	testLogger.Log("-----------------------------")

	// 4. 【核心改造】分数聚合
	testLogger.Log("阶段4: 开始进行Top-K分数聚合...")
	submissionChunkScores := make(map[string][]float32)

	// 收集每个submission的所有reranked chunk分数
	for _, res := range candidatesForRerank {
		if newScore, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], newScore)
			}
		}
	}
	testLogger.Log("聚合了 %d 份独立简历的所有chunk分数。", len(submissionChunkScores))

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
	testLogger.Log("使用Top-%d平均分策略（动态分母），计算出 %d 份简历的最终综合得分。", topK, len(finalSubmissionScores))

	// 5. 最终评测
	goldenTruth := getGoBackendGoldenTruthSet()

	foundTier1 := make(map[string]float32)
	foundTier2 := make(map[string]float32)
	foundTier3 := make(map[string]float32)
	unknownFound := make(map[string]float32)

	for uuid, score := range finalSubmissionScores {
		if goldenTruth.Tier1[uuid] {
			foundTier1[uuid] = score
		} else if goldenTruth.Tier2[uuid] {
			foundTier2[uuid] = score
		} else if goldenTruth.Tier3[uuid] {
			foundTier3[uuid] = score
		} else {
			unknownFound[uuid] = score
		}
	}

	// 输出评测报告
	testLogger.Log("=== Go后端岗位评测报告 (Top-K聚合策略) ===")
	testLogger.Log("==========================================")

	// Tier 1 评测
	tier1Recall := float64(len(foundTier1)) / float64(len(goldenTruth.Tier1)) * 100
	testLogger.Log("高相关 (Tier 1):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier1))
	testLogger.Log("  - 召回数量: %d", len(foundTier1))
	testLogger.Log("  - 召回率: %.2f%%", tier1Recall)

	// Tier 2 评测
	tier2Recall := float64(len(foundTier2)) / float64(len(goldenTruth.Tier2)) * 100
	testLogger.Log("中相关 (Tier 2):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier2))
	testLogger.Log("  - 召回数量: %d", len(foundTier2))
	testLogger.Log("  - 召回率: %.2f%%", tier2Recall)

	// Tier 3 评测 (噪音)
	tier3Noise := float64(len(foundTier3)) / float64(len(goldenTruth.Tier3)) * 100
	testLogger.Log("低相关/噪音 (Tier 3):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier3))
	testLogger.Log("  - 召回数量: %d", len(foundTier3))
	testLogger.Log("  - 噪音占比: %.2f%% (越低越好)", tier3Noise)

	// 其他项
	testLogger.Log("未分类项 (Unknown):")
	testLogger.Log("  - 召回数量: %d", len(unknownFound))

	testLogger.Log("==========================================")

	// --- 详细分数分布分析 (基于聚合后分数) ---
	logScoreDetails(testLogger, "高相关 (Tier 1)", foundTier1)
	logScoreDetails(testLogger, "中相关 (Tier 2)", foundTier2)
	logScoreDetails(testLogger, "低相关/噪音 (Tier 3)", foundTier3)
	logScoreDetails(testLogger, "未分类项 (Unknown)", unknownFound)

	testLogger.Log("==========================================")
	testLogger.Log("测试完成")
}

// logScoreDetails 是一个辅助函数，用于分析和记录每个层级的分数详情
func logScoreDetails(testLogger *TestLogger, tierName string, foundItems map[string]float32) {
	if len(foundItems) == 0 {
		testLogger.Log("\n%s: 未召回任何简历。", tierName)
		return
	}

	var scores []float32
	var scoreSum float32
	minScore := float32(2.0)  // 分数通常在-1到1之间，2.0是安全的初始值
	maxScore := float32(-2.0) // -2.0是安全的初始值

	var uuids []string
	for uuid := range foundItems {
		uuids = append(uuids, uuid)
	}
	// 按分数降序排序，便于查看
	sort.Slice(uuids, func(i, j int) bool {
		return foundItems[uuids[i]] > foundItems[uuids[j]]
	})

	testLogger.Log("\n%s (召回 %d 份):", tierName, len(foundItems))
	for _, uuid := range uuids {
		score := foundItems[uuid]
		testLogger.Log("  - UUID: %s, Score: %.4f", uuid, score)
		scores = append(scores, score)
		scoreSum += score
		if score < minScore {
			minScore = score
		}
		if score > maxScore {
			maxScore = score
		}
	}

	avgScore := scoreSum / float32(len(scores))
	testLogger.Log("  -> 统计: 最高分=%.4f, 最低分=%.4f, 平均分=%.4f", maxScore, minScore, avgScore)
}

// TestSearchStrategyComparison 对比三种不同的搜索与排序策略
func TestSearchStrategyComparison(t *testing.T) {
	// 创建测试日志记录器
	testLogger, err := NewTestLogger(t, "SearchStrategyComparison")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始对比三种搜索策略：1. 仅向量搜索 2. 向量+Rerank 3. 向量+Rerank+聚合")

	// --- 1. 共享的测试设置 ---
	ctx := context.Background()
	appCoreLogger.Init(appCoreLogger.Config{Level: "warn", Format: "pretty"})
	glog.SetLogger(hertzadapter.From(appCoreLogger.Logger))
	glog.SetLevel(glog.LevelDebug)

	cfg, err := config.LoadConfigFromFileOnly(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}
	if s.Qdrant == nil || cfg.Aliyun.APIKey == "" || cfg.Aliyun.APIKey == "your-api-key" || !cfg.Reranker.Enabled {
		t.Skip("跳过测试：Qdrant、阿里云API Key或Reranker未配置")
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。` // 使用简化的JD以专注于策略对比

	vectors, err := embedder.EmbedStrings(context.Background(), []string{jobDesc})
	require.NoError(t, err, "为岗位描述生成向量失败")
	vector := vectors[0]

	goldenTruth := getGoBackendGoldenTruthSet()

	// --- 2. 一次性执行召回和重排，复用结果 ---
	const recallLimitForRerank = 300
	const vectorSearchOnlyLimit = 150 // 根据建议调整limit

	testLogger.Log("共享步骤: 向量搜索 (召回 Limit: %d)...", recallLimitForRerank)
	initialResults, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimitForRerank, nil)
	require.NoError(t, err, "向量搜索失败")
	testLogger.Log("共享步骤: 向量搜索完成，召回 %d 个初步结果。", len(initialResults))

	scoreThreshold := float32(0.50)
	var candidatesForRerank []storage.SearchResult
	for _, res := range initialResults {
		if res.Score >= scoreThreshold {
			candidatesForRerank = append(candidatesForRerank, res)
		}
	}
	testLogger.Log("共享步骤: 海选过滤完成，剩下 %d 个候选结果。", len(candidatesForRerank))

	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, candidatesForRerank, t, testLogger)
	if err != nil || rerankScores == nil {
		t.Fatalf("Reranker调用失败或未返回结果，测试无法继续: %v", err)
	}
	testLogger.Log("共享步骤: Reranker精排完成。")

	// --- 3. 分别执行和评估三种策略 ---

	// 策略 1: 仅向量搜索 (取最高分, limit=150, threshold=0.50)
	testLogger.Log("开始评估策略1 (仅向量搜索)，分析召回结果的前 %d 项，并应用score_threshold=%.2f...", vectorSearchOnlyLimit, scoreThreshold)
	finalScoresStrategy1 := make(map[string]float32)
	resultsForStrategy1 := initialResults
	if len(resultsForStrategy1) > vectorSearchOnlyLimit {
		resultsForStrategy1 = resultsForStrategy1[:vectorSearchOnlyLimit]
	}
	for _, res := range resultsForStrategy1 {
		// 应用与Rerank策略相同的分数阈值
		if res.Score >= scoreThreshold {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				if currentScore, ok := finalScoresStrategy1[uuid]; !ok || res.Score > currentScore {
					finalScoresStrategy1[uuid] = res.Score
				}
			}
		}
	}
	evaluateAndLogReport(testLogger, "策略 1: 仅向量搜索 (Max Score, Limit 150, Threshold 0.50)", finalScoresStrategy1, goldenTruth)

	// 策略 2: 向量搜索 + Rerank (取最高分)
	finalScoresStrategy2 := make(map[string]float32)
	for _, res := range candidatesForRerank {
		if rerankScore, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				if currentScore, ok := finalScoresStrategy2[uuid]; !ok || rerankScore > currentScore {
					finalScoresStrategy2[uuid] = rerankScore
				}
			}
		}
	}
	evaluateAndLogReport(testLogger, "策略 2: 向量 + Rerank (Max Score, Recall Limit 300)", finalScoresStrategy2, goldenTruth)

	// 策略 3: 向量搜索 + Rerank + 短板惩罚聚合
	submissionChunkScores := make(map[string][]float32)
	for _, res := range candidatesForRerank {
		if newScore, ok := rerankScores[res.ID]; ok {
			uuid, _ := res.Payload["submission_uuid"].(string)
			if uuid != "" {
				submissionChunkScores[uuid] = append(submissionChunkScores[uuid], newScore)
			}
		}
	}
	finalScoresStrategy3 := make(map[string]float32)
	const topK = 3
	for uuid, scores := range submissionChunkScores {
		sort.Slice(scores, func(i, j int) bool { return scores[i] > scores[j] })
		effectiveK := topK
		if len(scores) < effectiveK {
			effectiveK = len(scores)
		}
		var sum float32
		for i := 0; i < effectiveK; i++ {
			sum += scores[i]
		}
		// 动态分母聚合：避免对短简历进行双重惩罚
		finalScoresStrategy3[uuid] = sum / float32(effectiveK)
	}
	evaluateAndLogReport(testLogger, "策略 3: 向量 + Rerank + 聚合 (动态分母)", finalScoresStrategy3, goldenTruth)

	testLogger.Log("所有策略对比测试完成。")
}

// evaluateAndLogReport 是一个辅助函数，用于执行评测并记录标准化的报告
func evaluateAndLogReport(testLogger *TestLogger, strategyName string, finalScores map[string]float32, goldenTruth GoldenTruthSet) {
	foundTier1 := make(map[string]float32)
	foundTier2 := make(map[string]float32)
	foundTier3 := make(map[string]float32)
	unknownFound := make(map[string]float32)

	for uuid, score := range finalScores {
		if goldenTruth.Tier1[uuid] {
			foundTier1[uuid] = score
		} else if goldenTruth.Tier2[uuid] {
			foundTier2[uuid] = score
		} else if goldenTruth.Tier3[uuid] {
			foundTier3[uuid] = score
		} else {
			unknownFound[uuid] = score
		}
	}

	// --- 输出评测报告 ---
	testLogger.Log("\n\n===== [评测报告: %s] =====", strategyName)
	testLogger.Log("====================================================")

	// Tier 1 评测
	tier1Recall := 0.0
	if len(goldenTruth.Tier1) > 0 {
		tier1Recall = float64(len(foundTier1)) / float64(len(goldenTruth.Tier1)) * 100
	}
	testLogger.Log("高相关 (Tier 1):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier1))
	testLogger.Log("  - 召回数量: %d", len(foundTier1))
	testLogger.Log("  - 召回率: %.2f%%", tier1Recall)

	// Tier 2 评测
	tier2Recall := 0.0
	if len(goldenTruth.Tier2) > 0 {
		tier2Recall = float64(len(foundTier2)) / float64(len(goldenTruth.Tier2)) * 100
	}
	testLogger.Log("中相关 (Tier 2):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier2))
	testLogger.Log("  - 召回数量: %d", len(foundTier2))
	testLogger.Log("  - 召回率: %.2f%%", tier2Recall)

	// Tier 3 评测 (噪音)
	tier3Noise := 0.0
	if len(goldenTruth.Tier3) > 0 {
		tier3Noise = float64(len(foundTier3)) / float64(len(goldenTruth.Tier3)) * 100
	}
	testLogger.Log("低相关/噪音 (Tier 3):")
	testLogger.Log("  - 目标总数: %d", len(goldenTruth.Tier3))
	testLogger.Log("  - 召回数量: %d", len(foundTier3))
	testLogger.Log("  - 噪音占比: %.2f%% (越低越好)", tier3Noise)

	// 其他项
	testLogger.Log("未分类项 (Unknown):")
	testLogger.Log("  - 召回数量: %d", len(unknownFound))
	testLogger.Log("----------------------------------------------------")

	// --- 详细分数分布分析 ---
	logScoreDetails(testLogger, "高相关 (Tier 1)", foundTier1)
	logScoreDetails(testLogger, "中相关 (Tier 2)", foundTier2)
	logScoreDetails(testLogger, "低相关/噪音 (Tier 3)", foundTier3)
	logScoreDetails(testLogger, "未分类项 (Unknown)", unknownFound)

	testLogger.Log("====================================================\n\n")
}
