package handler_test // 测试包，用于测试handler包中的功能

import (
	"bytes"         // 用于处理字节缓冲区
	"context"       // 用于控制请求的上下文
	"encoding/json" // 用于JSON编解码
	"fmt"           // 用于格式化输出
	"io"            // 提供I/O原语
	"math"          // 提供数学函数
	"net/http"      // HTTP客户端和服务器实现
	"os"            // 提供操作系统功能
	"sort"          // 提供排序函数
	"testing"       // Go测试框架

	"ai-agent-go/internal/config"  // 项目配置包
	"ai-agent-go/internal/parser"  // 解析器包
	"ai-agent-go/internal/storage" // 存储相关包

	"github.com/stretchr/testify/require" // 测试断言库
)

// TestRerankerEffectiveness 专门用于评估Reranker模型对搜索结果的实际影响。
// 它会记录每个文档块在Rerank前后的分数，以便进行直接对比。
/*
 * 完整流程:
 * 召回300  → 轻量粗排120 (单批)  → 60×2 分批精排
 * →  批内 z-score  →  RRF 融合  →  简历级(uuid)聚合  → 评测
 */
func TestRerankerEffectiveness(t *testing.T) { // 定义一个测试函数，用于评估Reranker模型的有效性
	// ---------- 0. 通用初始化 ----------
	testLogger, err := NewTestLogger(t, "reranker_effectiveness_analysis") // 创建一个新的测试日志记录器，并命名为 "reranker_effectiveness_analysis"
	require.NoError(t, err, "创建测试日志记录器失败")                                 // 断言在创建日志记录器过程中没有发生错误，如果出错则测试失败
	defer testLogger.Close()

	testLogger.Log("开始测试Reranker模型有效性...")
	ctx := context.Background()

	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	if os.Getenv("CI") != "" || s.Qdrant == nil || cfg.Reranker.URL == "" {
		t.Skip("跳过：CI 环境或依赖未配置")
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

	// ---------- 1. JD 向量与黄金集 ----------
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
	vectors, err := embedder.EmbedStrings(ctx, []string{jobDesc})
	require.NoError(t, err)
	vector := vectors[0]

	goldenSet := getGoBackendGoldenTruthSet()
	testLogger.Log("黄金评测集已加载: Tier1=%d, Tier2=%d, Tier3=%d", len(goldenSet.Tier1), len(goldenSet.Tier2), len(goldenSet.Tier3))

	// ---------- 2. 基线 & Top-100 单批 ----------
	t.Run("A.1-VectorOnly-Top300", func(t *testing.T) {
		const recallLimit = 300
		retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil)
		require.NoError(t, err, "向量搜索失败 (VectorOnly)")

		// 聚合到简历级别进行评测 (仅使用向量分数)
		submissionScores := make(map[string]float32)
		for _, doc := range retrievedDocs {
			if uuid, ok := doc.Payload["submission_uuid"].(string); ok {
				if currentScore, exists := submissionScores[uuid]; !exists || doc.Score > currentScore {
					submissionScores[uuid] = doc.Score
				}
			}
		}
		var rankedSubmissions []RankedSubmission
		for uuid, score := range submissionScores {
			rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: score})
		}
		sort.Slice(rankedSubmissions, func(i, j int) bool {
			return rankedSubmissions[i].Score > rankedSubmissions[j].Score
		})

		evaluateAndLogMetrics(t, testLogger, "A.1-VectorOnly-Top300", rankedSubmissions, &goldenSet)
	})

	t.Run("A.2-Reranked-Top300", func(t *testing.T) {
		const recallLimit = 300
		retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil)
		require.NoError(t, err, "向量搜索失败 (Baseline)")
		testLogger.Log("向量数据库为场景A召回了 %d 个文档块", len(retrievedDocs))

		rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, retrievedDocs, t, testLogger)
		require.NoError(t, err, "调用Reranker服务失败 (Baseline)")
		testLogger.Log("基线Reranker服务成功返回了 %d 个分数", len(rerankScores))

		// 聚合到简历级别进行评测
		submissionScores := make(map[string]float32)
		for _, doc := range retrievedDocs {
			rerankScore, ok := rerankScores[doc.ID]
			if !ok {
				continue // 如果 chunk 没有 rerank 分数，则忽略
			}
			if uuid, ok := doc.Payload["submission_uuid"].(string); ok {
				if currentScore, exists := submissionScores[uuid]; !exists || rerankScore > currentScore {
					submissionScores[uuid] = rerankScore
				}
			}
		}
		var rankedSubmissions []RankedSubmission
		for uuid, score := range submissionScores {
			rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: score})
		}
		sort.Slice(rankedSubmissions, func(i, j int) bool {
			return rankedSubmissions[i].Score > rankedSubmissions[j].Score
		})

		evaluateAndLogMetrics(t, testLogger, "A.2-Reranked-Top300", rankedSubmissions, &goldenSet)
	})

	t.Run("A.3-LinearFusion-Top300", func(t *testing.T) {
		const recallLimit = 300
		retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil)
		require.NoError(t, err, "向量搜索失败")
		rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, retrievedDocs, t, testLogger)
		require.NoError(t, err, "调用Reranker服务失败")

		// 同时拥有向量和rerank分数的文档
		var docsForFusion []fusedDoc

		// 聚合到简历级别, 保留两种分数
		aggMap := make(map[string]fusedDoc)
		for _, doc := range retrievedDocs {
			uuid, ok := doc.Payload["submission_uuid"].(string)
			if !ok {
				continue
			}
			rerankScore, hasRerank := rerankScores[doc.ID]
			if !hasRerank {
				continue
			}

			// 以最高 rerank score 对应的 vector score 为准
			if existing, ok := aggMap[uuid]; !ok || rerankScore > existing.rerankScore {
				aggMap[uuid] = fusedDoc{
					uuid:        uuid,
					vectorScore: doc.Score,
					rerankScore: rerankScore,
				}
			}
		}
		for _, v := range aggMap {
			docsForFusion = append(docsForFusion, v)
		}

		rankedSubmissions := linearFusion(docsForFusion, 0.5, 0.5)
		evaluateAndLogMetrics(t, testLogger, "A.3-LinearFusion-Top300", rankedSubmissions, &goldenSet)
	})

	t.Run("B.1-VectorOnly-Top100", func(t *testing.T) {
		const recallLimit = 100
		retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil)
		require.NoError(t, err, "向量搜索失败 (Optimized)")

		// 聚合到简历级别进行评测 (仅使用向量分数)
		submissionScores := make(map[string]float32)
		for _, doc := range retrievedDocs {
			if uuid, ok := doc.Payload["submission_uuid"].(string); ok {
				if currentScore, exists := submissionScores[uuid]; !exists || doc.Score > currentScore {
					submissionScores[uuid] = doc.Score
				}
			}
		}
		var rankedSubmissions []RankedSubmission
		for uuid, score := range submissionScores {
			rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: score})
		}
		sort.Slice(rankedSubmissions, func(i, j int) bool {
			return rankedSubmissions[i].Score > rankedSubmissions[j].Score
		})

		evaluateAndLogMetrics(t, testLogger, "B.1-VectorOnly-Top100", rankedSubmissions, &goldenSet)
	})

	t.Run("B.2-Reranked-Top100", func(t *testing.T) {
		const recallLimit = 100
		retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil)
		require.NoError(t, err, "向量搜索失败 (Optimized)")
		testLogger.Log("向量数据库为场景B召回了 %d 个文档块", len(retrievedDocs))

		rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, retrievedDocs, t, testLogger)
		require.NoError(t, err, "调用Reranker服务失败 (Optimized)")
		testLogger.Log("优化后Reranker服务成功返回了 %d 个分数", len(rerankScores))

		// 聚合到简历级别进行评测
		submissionScores := make(map[string]float32)
		for _, doc := range retrievedDocs {
			rerankScore, ok := rerankScores[doc.ID]
			if !ok {
				continue
			}
			if uuid, ok := doc.Payload["submission_uuid"].(string); ok {
				if currentScore, exists := submissionScores[uuid]; !exists || rerankScore > currentScore {
					submissionScores[uuid] = rerankScore
				}
			}
		}
		var rankedSubmissions []RankedSubmission
		for uuid, score := range submissionScores {
			rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: score})
		}
		sort.Slice(rankedSubmissions, func(i, j int) bool {
			return rankedSubmissions[i].Score > rankedSubmissions[j].Score
		})

		evaluateAndLogMetrics(t, testLogger, "B.2-Reranked-Top100", rankedSubmissions, &goldenSet)
	})

	t.Run("B.3-LinearFusion-Top100", func(t *testing.T) {
		const recallLimit = 100
		retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil)
		require.NoError(t, err, "向量搜索失败")
		rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, retrievedDocs, t, testLogger)
		require.NoError(t, err, "调用Reranker服务失败")

		var docsForFusion []fusedDoc
		aggMap := make(map[string]fusedDoc)
		for _, doc := range retrievedDocs {
			uuid, ok := doc.Payload["submission_uuid"].(string)
			if !ok {
				continue
			}
			rerankScore, hasRerank := rerankScores[doc.ID]
			if !hasRerank {
				continue
			}
			if existing, ok := aggMap[uuid]; !ok || rerankScore > existing.rerankScore {
				aggMap[uuid] = fusedDoc{
					uuid:        uuid,
					vectorScore: doc.Score,
					rerankScore: rerankScore,
				}
			}
		}
		for _, v := range aggMap {
			docsForFusion = append(docsForFusion, v)
		}

		rankedSubmissions := linearFusion(docsForFusion, 0.5, 0.5)
		evaluateAndLogMetrics(t, testLogger, "B.3-LinearFusion-Top100", rankedSubmissions, &goldenSet)
	})

	// ---------- 3. 场景 C: AggregateFirst-Rerank-And-Fusion ----------
	t.Run("C-AggregateFirst-Rerank-And-Fusion", func(t *testing.T) {
		const (
			recallK              = 500  // 向量初召回
			candidateResumesTopN = 120  // 聚合后保留的候选简历数
			batchSize            = 50   // 单批喂 Reranker
			rrfK                 = 60.0 // RRF 超参
			wVector              = 0.3  // 线性融合中向量分数的权重
			wRerank              = 0.7  // 线性融合中Rerank分数的权重
		)

		// 3.1 向量召回 K 个 chunk
		rawDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallK, nil)
		require.NoError(t, err)
		testLogger.Log("[场景 C] 步骤 1: 成功召回 %d 个文档块", len(rawDocs))

		// 3.2 按 submission_uuid 分组, 取每组 vector 分数最高的 chunk 作代表
		bestPerResume := make(map[string]storage.SearchResult)
		for _, doc := range rawDocs {
			uuid, ok := doc.Payload["submission_uuid"].(string)
			if !ok {
				continue
			}
			if existing, ok := bestPerResume[uuid]; !ok || doc.Score > existing.Score {
				bestPerResume[uuid] = doc
			}
		}
		testLogger.Log("[场景 C] 步骤 2: 从召回结果中识别出 %d 份独立简历, 并选出其最佳chunk", len(bestPerResume))

		// 3.3 取 vector_score 前 N 的简历 (的最佳chunk)
		var candidateBestChunks []storage.SearchResult
		for _, doc := range bestPerResume {
			candidateBestChunks = append(candidateBestChunks, doc)
		}
		sort.Slice(candidateBestChunks, func(i, j int) bool { return candidateBestChunks[i].Score > candidateBestChunks[j].Score })

		if len(candidateBestChunks) > candidateResumesTopN {
			candidateBestChunks = candidateBestChunks[:candidateResumesTopN]
		}
		testLogger.Log("[场景 C] 步骤 3: 筛选出 Top %d 的简历用于Rerank", len(candidateBestChunks))

		// 3.4 批量 Rerank (改为串行以避免并发问题)
		var batches [][]storage.SearchResult
		for i := 0; i < len(candidateBestChunks); i += batchSize {
			end := i + batchSize
			if end > len(candidateBestChunks) {
				end = len(candidateBestChunks)
			}
			batches = append(batches, candidateBestChunks[i:end])
		}
		testLogger.Log("[场景 C] 步骤 4.1: 将 %d 份简历切成 %d 批 (每批~%d) 进行Rerank", len(candidateBestChunks), len(batches), batchSize)

		rerankScores := make(map[string]float32)
		for _, batch := range batches {
			scores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, batch, t, testLogger)
			require.NoError(t, err)
			for id, score := range scores {
				rerankScores[id] = score
			}
		}
		testLogger.Log("[场景 C] 步骤 4.2: Rerank处理完成, 返回 %d 个分数", len(rerankScores))

		// 3.5 准备用于多策略排序的融合数据
		type resumeRankInfo struct {
			uuid        string
			vectorScore float32
			rerankScore float32
		}
		var resumeRankingData []resumeRankInfo
		for _, chunk := range candidateBestChunks {
			uuid := chunk.Payload["submission_uuid"].(string)
			rs, ok := rerankScores[chunk.ID]
			if !ok {
				// 如果 reranker 没有返回分数, 则不应参与融合评估
				continue
			}
			resumeRankingData = append(resumeRankingData, resumeRankInfo{
				uuid:        uuid,
				vectorScore: chunk.Score,
				rerankScore: rs,
			})
		}
		testLogger.Log("[场景 C] 步骤 5: 已生成 %d 条可用于融合排序的数据", len(resumeRankingData))

		// --- 策略 1: 仅使用 Rerank 分数排序 ---
		t.Run("RerankOnly", func(t *testing.T) {
			dataForSort := append([]resumeRankInfo(nil), resumeRankingData...)
			sort.SliceStable(dataForSort, func(i, j int) bool {
				return dataForSort[i].rerankScore > dataForSort[j].rerankScore
			})
			var rankedSubmissions []RankedSubmission
			for _, item := range dataForSort {
				rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: item.uuid, Score: item.rerankScore})
			}
			evaluateAndLogMetrics(t, testLogger, "C.1-AggFirst-RerankOnly", rankedSubmissions, &goldenSet)
		})

		// --- 策略 2: 线性融合 (Vector * w1 + Rerank * w2) ---
		t.Run("LinearFusion", func(t *testing.T) {
			dataForSort := append([]resumeRankInfo(nil), resumeRankingData...)
			// 为防止原始分数范围影响权重, 先进行Min-Max归一化
			var minVec, maxVec, minRerank, maxRerank float32 = math.MaxFloat32, -math.MaxFloat32, math.MaxFloat32, -math.MaxFloat32
			for _, item := range dataForSort {
				if item.vectorScore < minVec {
					minVec = item.vectorScore
				}
				if item.vectorScore > maxVec {
					maxVec = item.vectorScore
				}
				if item.rerankScore < minRerank {
					minRerank = item.rerankScore
				}
				if item.rerankScore > maxRerank {
					maxRerank = item.rerankScore
				}
			}
			normalize := func(val, min, max float32) float32 {
				if max-min == 0 {
					return 0
				}
				return (val - min) / (max - min)
			}

			var rankedSubmissions []RankedSubmission
			for _, item := range dataForSort {
				normVec := normalize(item.vectorScore, minVec, maxVec)
				normRerank := normalize(item.rerankScore, minRerank, maxRerank)
				fusedScore := wVector*normVec + wRerank*normRerank
				rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: item.uuid, Score: fusedScore})
			}

			sort.Slice(rankedSubmissions, func(i, j int) bool { return rankedSubmissions[i].Score > rankedSubmissions[j].Score })
			evaluateAndLogMetrics(t, testLogger, "C.2-AggFirst-LinearFusion", rankedSubmissions, &goldenSet)
		})

		// --- 策略 3: RRF 融合 ---
		t.Run("RRF", func(t *testing.T) {
			dataForSort := append([]resumeRankInfo(nil), resumeRankingData...)
			// 按 vector score 排名
			sort.SliceStable(dataForSort, func(i, j int) bool { return dataForSort[i].vectorScore > dataForSort[j].vectorScore })
			rankVec := make(map[string]int)
			for i, item := range dataForSort {
				rankVec[item.uuid] = i + 1
			}
			// 按 rerank score 排名
			sort.SliceStable(dataForSort, func(i, j int) bool { return dataForSort[i].rerankScore > dataForSort[j].rerankScore })
			rankRerank := make(map[string]int)
			for i, item := range dataForSort {
				rankRerank[item.uuid] = i + 1
			}

			// 计算 RRF 分数
			rrfScores := make(map[string]float64)
			for _, item := range dataForSort {
				uuid := item.uuid
				rrfScores[uuid] = (1.0 / (rrfK + float64(rankVec[uuid]))) + (1.0 / (rrfK + float64(rankRerank[uuid])))
			}

			var rankedSubmissions []RankedSubmission
			for uuid, score := range rrfScores {
				rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: float32(score)})
			}
			sort.Slice(rankedSubmissions, func(i, j int) bool { return rankedSubmissions[i].Score > rankedSubmissions[j].Score })
			evaluateAndLogMetrics(t, testLogger, "C.3-AggFirst-RRF", rankedSubmissions, &goldenSet)
		})
	})

	// ---------- 4. 场景 D: FullRerank-Then-Aggregate (最贵，信息最全) ----------
	t.Run("D-FullRerank-Then-Aggregate", func(t *testing.T) {
		const (
			recallK   = 500  // 向量初召回
			batchSize = 50   // 单批喂 Reranker
			rrfK      = 60.0 // RRF 超参
		)

		// D.1 向量召回 K 个 chunk
		rawDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallK, nil)
		require.NoError(t, err)
		testLogger.Log("[场景 D] 步骤 1: 成功召回 %d 个文档块", len(rawDocs))

		// D.2 批量 Rerank (改为串行以避免并发问题)
		var batches [][]storage.SearchResult
		for i := 0; i < len(rawDocs); i += batchSize {
			end := i + batchSize
			if end > len(rawDocs) {
				end = len(rawDocs)
			}
			batches = append(batches, rawDocs[i:end])
		}
		testLogger.Log("[场景 D] 步骤 2.1: 将 %d 个块切成 %d 批 (每批~%d) 进行Rerank", len(rawDocs), len(batches), batchSize)

		rerankScores := make(map[string]float32)
		for _, batch := range batches {
			scores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, batch, t, testLogger)
			require.NoError(t, err)
			for id, score := range scores {
				rerankScores[id] = score
			}
		}
		testLogger.Log("[场景 D] 步骤 2.2: 全量Rerank完成, 获得 %d 个分数", len(rerankScores))

		// D.3 按 uuid 聚合
		uuidToChunks := make(map[string][]storage.SearchResult)
		for _, doc := range rawDocs {
			if uuid, ok := doc.Payload["submission_uuid"].(string); ok {
				uuidToChunks[uuid] = append(uuidToChunks[uuid], doc)
			}
		}
		testLogger.Log("[场景 D] 步骤 3: 已将所有块按 %d 份简历进行分组", len(uuidToChunks))

		// --- 策略 1: Max 聚合 ---
		t.Run("MaxAggregation", func(t *testing.T) {
			submissionScores := make(map[string]float32)
			for uuid, chunks := range uuidToChunks {
				maxScore := float32(-1e9)
				for _, chunk := range chunks {
					if score, ok := rerankScores[chunk.ID]; ok {
						if score > maxScore {
							maxScore = score
						}
					}
				}
				if maxScore > -1e9 {
					submissionScores[uuid] = maxScore
				}
			}
			var rankedSubmissions []RankedSubmission
			for uuid, score := range submissionScores {
				rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: score})
			}
			sort.Slice(rankedSubmissions, func(i, j int) bool { return rankedSubmissions[i].Score > rankedSubmissions[j].Score })
			evaluateAndLogMetrics(t, testLogger, "D.1-FullRerank-MaxAgg", rankedSubmissions, &goldenSet)
		})

		// --- 策略 2: Mean 聚合 ---
		t.Run("MeanAggregation", func(t *testing.T) {
			submissionScores := make(map[string]float32)
			for uuid, chunks := range uuidToChunks {
				var sumScore float32
				var count int
				for _, chunk := range chunks {
					if score, ok := rerankScores[chunk.ID]; ok {
						sumScore += score
						count++
					}
				}
				if count > 0 {
					submissionScores[uuid] = sumScore / float32(count)
				}
			}
			var rankedSubmissions []RankedSubmission
			for uuid, score := range submissionScores {
				rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: score})
			}
			sort.Slice(rankedSubmissions, func(i, j int) bool { return rankedSubmissions[i].Score > rankedSubmissions[j].Score })
			evaluateAndLogMetrics(t, testLogger, "D.2-FullRerank-MeanAgg", rankedSubmissions, &goldenSet)
		})

		// --- 策略 3: RRF 聚合 (基于chunk排名) ---
		t.Run("ChunkRRF_Aggregation", func(t *testing.T) {
			submissionScores := make(map[string]float64)
			for uuid, chunks := range uuidToChunks {
				// 为当前简历的所有块按 rerank 分数排序
				sort.Slice(chunks, func(i, j int) bool {
					scoreI, _ := rerankScores[chunks[i].ID]
					scoreJ, _ := rerankScores[chunks[j].ID]
					return scoreI > scoreJ
				})
				// 计算 RRF 分数
				var rrfScore float64
				for rank, chunk := range chunks {
					if _, ok := rerankScores[chunk.ID]; ok {
						rrfScore += 1.0 / (rrfK + float64(rank+1))
					}
				}
				if rrfScore > 0 {
					submissionScores[uuid] = rrfScore
				}
			}

			var rankedSubmissions []RankedSubmission
			for uuid, score := range submissionScores {
				rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: uuid, Score: float32(score)})
			}
			sort.Slice(rankedSubmissions, func(i, j int) bool { return rankedSubmissions[i].Score > rankedSubmissions[j].Score })
			evaluateAndLogMetrics(t, testLogger, "D.3-FullRerank-ChunkRRFAgg", rankedSubmissions, &goldenSet)
		})
	})
}

// --- 融合与评估辅助函数 ---

// fusedDoc 结构体用于存储同时具有向量分数和重排序分数的文档信息
type fusedDoc struct {
	uuid        string  // 简历唯一标识符
	vectorScore float32 // 向量搜索得分
	rerankScore float32 // 重排序得分
}

// linearFusion 对一组包含两种分数的文档进行归一化和线性融合
// 参数docs为待融合的文档列表，wVector和wRerank分别为向量分数和重排序分数的权重
func linearFusion(docs []fusedDoc, wVector, wRerank float32) []RankedSubmission {
	if len(docs) == 0 {
		return nil
	}
	// 为防止原始分数范围影响权重, 先进行Min-Max归一化
	var minVec, maxVec, minRerank, maxRerank float32 = math.MaxFloat32, -math.MaxFloat32, math.MaxFloat32, -math.MaxFloat32
	for _, item := range docs {
		if item.vectorScore < minVec {
			minVec = item.vectorScore
		}
		if item.vectorScore > maxVec {
			maxVec = item.vectorScore
		}
		if item.rerankScore < minRerank {
			minRerank = item.rerankScore
		}
		if item.rerankScore > maxRerank {
			maxRerank = item.rerankScore
		}
	}
	normalize := func(val, min, max float32) float32 {
		if max-min == 0 {
			return 0
		}
		return (val - min) / (max - min)
	}

	var rankedSubmissions []RankedSubmission
	for _, item := range docs {
		normVec := normalize(item.vectorScore, minVec, maxVec)
		normRerank := normalize(item.rerankScore, minRerank, maxRerank)
		fusedScore := wVector*normVec + wRerank*normRerank
		rankedSubmissions = append(rankedSubmissions, RankedSubmission{UUID: item.uuid, Score: fusedScore})
	}

	sort.Slice(rankedSubmissions, func(i, j int) bool { return rankedSubmissions[i].Score > rankedSubmissions[j].Score })
	return rankedSubmissions
}

// evaluateAndLogMetrics 封装了评估和日志记录的逻辑。
// 它接收一个按分数排序的简历列表，并根据黄金集计算性能指标。
// 参数t为测试上下文，testLogger为日志记录器，scenarioName为测试场景名称，
// rankedSubmissions为排序后的简历列表，goldenSet为黄金标准集
func evaluateAndLogMetrics(t *testing.T, testLogger *TestLogger, scenarioName string, rankedSubmissions []RankedSubmission, goldenSet *GoldenTruthSet) {
	testLogger.Log("开始为场景 [%s] 计算性能指标...", scenarioName) // 记录开始计算指标的日志

	if len(rankedSubmissions) == 0 { // 检查结果列表是否为空
		testLogger.Log("警告: 场景 [%s] 的排名结果为空，跳过指标计算。", scenarioName)
		return
	}

	// 为每个结果分配其在黄金集中的相关性等级
	hasHit := false // 标记是否有命中黄金集的结果
	for i := range rankedSubmissions {
		uuid := rankedSubmissions[i].UUID
		if goldenSet.Tier1[uuid] { // 检查是否在Tier1中
			rankedSubmissions[i].RelevanceTier = "Tier1" // 标记为最高相关性
			hasHit = true
		} else if goldenSet.Tier2[uuid] { // 检查是否在Tier2中
			rankedSubmissions[i].RelevanceTier = "Tier2" // 标记为中等相关性
			hasHit = true
		} else if goldenSet.Tier3[uuid] { // 检查是否在Tier3中
			rankedSubmissions[i].RelevanceTier = "Tier3" // 标记为低相关性
			hasHit = true
		} else {
			rankedSubmissions[i].RelevanceTier = "N/A" // 标记为不相关
		}
	}

	// 如果没有命中任何黄金集条目，记录警告
	if !hasHit {
		testLogger.Log("警告: 在场景 [%s] 中，排名结果未命中任何黄金集条目。性能指标可能无意义。", scenarioName)
	}

	// 打印 Top-10 结果以供快速检查
	topN := 10 // 默认展示前10个结果
	if len(rankedSubmissions) < topN {
		topN = len(rankedSubmissions) // 如果结果不足10个，则展示所有结果
	}
	testLogger.LogObject(fmt.Sprintf("[%s] Top %d ranked resumes", scenarioName, topN), rankedSubmissions[:topN])

	// 计算并记录Top-10性能指标
	metrics10 := calculatePerformanceMetrics(rankedSubmissions, goldenSet, 10)
	testLogger.Log("[%s] Performance Metrics @10: Precision=%.4f, Recall=%.4f, NDCG=%.4f, MRR=%.4f",
		scenarioName, metrics10.Precision, metrics10.Recall, metrics10.NDCG, metrics10.MRR)

	// 计算并记录Top-30性能指标
	metrics30 := calculatePerformanceMetrics(rankedSubmissions, goldenSet, 30)
	testLogger.Log("[%s] Performance Metrics @30: Precision=%.4f, Recall=%.4f, NDCG=%.4f",
		scenarioName, metrics30.Precision, metrics30.Recall, metrics30.NDCG)
}

// callRerankerWithDocuments 是一个本地实现的 reranker 调用函数
func callRerankerWithDocuments(ctx context.Context, rerankerURL string, query string, documents []RerankDocument, t *testing.T, logger *TestLogger) (map[string]float32, error) {
	if rerankerURL == "" { // 检查reranker服务的URL是否已配置
		logger.Log("Reranker URL is not configured. Skipping rerank.") // 如果未配置，则记录日志并跳过
		return make(map[string]float32), nil                           // 返回一个空的map和nil错误
	}

	reqBody := RerankRequest{ // 创建一个Rerank请求体
		Query:     query,     // 设置查询语句
		Documents: documents, // 设置待重排序的文档
	}

	jsonData, err := json.Marshal(reqBody) // 将请求体序列化为JSON格式
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rerank request: %w", err) // 如果序列化失败，返回错误
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rerankerURL, bytes.NewBuffer(jsonData)) // 创建一个带上下文的HTTP POST请求
	if err != nil {
		return nil, fmt.Errorf("failed to create rerank request: %w", err) // 如果创建请求失败，返回错误
	}
	req.Header.Set("Content-Type", "application/json") // 设置请求头的内容类型为JSON

	client := &http.Client{}    // 创建一个HTTP客户端
	resp, err := client.Do(req) // 发送HTTP请求并获取响应
	if err != nil {
		return nil, fmt.Errorf("failed to call reranker service: %w", err) // 如果请求失败，返回错误
	}
	defer resp.Body.Close() // 确保在函数返回时关闭响应体

	if resp.StatusCode != http.StatusOK { // 检查响应的状态码是否为200 OK
		bodyBytes, _ := io.ReadAll(resp.Body)                                                                           // 读取响应体内容以供调试
		return nil, fmt.Errorf("reranker service returned non-OK status: %s, body: %s", resp.Status, string(bodyBytes)) // 如果状态码不为200，返回错误信息
	}

	var rerankedDocs []RerankedDocument                                      // 定义一个变量来存储解码后的重排序文档
	if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil { // 解码响应体中的JSON数据
		return nil, fmt.Errorf("failed to decode reranker response: %w", err) // 如果解码失败，返回错误
	}

	scores := make(map[string]float32) // 创建一个map来存储文档ID和对应的重排序分数
	for _, doc := range rerankedDocs { // 遍历所有重排序后的文档
		scores[doc.ID] = doc.RerankScore // 将文档ID和分数存入map
	}

	return scores, nil // 返回包含所有分数的map和nil错误
}

// --- 评估指标相关辅助函数 ---

// RankedSubmission 用于评估的排序后简历对象
type RankedSubmission struct {
	UUID          string  // 简历唯一标识符
	Score         float32 // 排序得分
	RelevanceTier string  // 相关性等级(Tier1/Tier2/Tier3/N/A)
}

// PerformanceMetrics 存储计算出的评估指标
type PerformanceMetrics struct {
	Precision float64 // 准确率：相关结果数/返回结果总数
	Recall    float64 // 召回率：相关结果数/所有相关结果总数
	NDCG      float64 // 归一化折损累积增益：评估排序质量
	MRR       float64 // 平均倒数排名：首个相关结果的排名质量
}

// getRelevanceScore 将黄金集中的Tier映射为数值分数用于NDCG计算
// 参数tier为相关性等级，返回对应的数值评分：Tier1=3.0, Tier2=2.0, Tier3=1.0, 其他=0.0
func getRelevanceScore(tier string) float64 { // 定义获取相关性分数的函数
	switch tier { // 根据输入的相关性等级字符串
	case "Tier1": // 如果是最高相关性
		return 3.0 // 返回3.0分
	case "Tier2": // 如果是中等相关性
		return 2.0 // 返回2.0分
	case "Tier3": // 如果是低相关性
		return 1.0 // 返回1.0分
	default: // 对于其他情况 (如 "N/A")
		return 0.0 // 返回0.0分
	}
}

// calculatePerformanceMetrics 计算各种排序评估指标
// 参数rankedSubmissions为排序后的简历列表，goldenSet为黄金标准集，k为评估的结果数量
func calculatePerformanceMetrics(rankedSubmissions []RankedSubmission, goldenSet *GoldenTruthSet, k int) PerformanceMetrics { // 定义计算性能指标的函数
	var hits, totalRelevant int // 定义命中数（仅计Tier1/2）和总相关数（Tier1+Tier2）
	var dcg, mrr float64        // 定义DCG（折损累积增益）和MRR（平均倒数排名）
	firstRelevantFound := false // 定义一个布尔标志，用于记录是否已找到第一个相关结果

	totalRelevant = len(goldenSet.Tier1) + len(goldenSet.Tier2) // 计算总的相关条目数（这里假定Tier1和Tier2是用户期望看到的相关结果）
	if totalRelevant == 0 {                                     // 如果黄金集中没有相关条目
		totalRelevant = 1 // 将其设置为1，以避免在计算召回率时发生除零错误
	}

	if k > len(rankedSubmissions) { // 检查k值是否超过了排序列表的总长度
		k = len(rankedSubmissions) // 如果是，则将k设置为列表的实际长度
	}

	// 计算前k个结果的评估指标
	for i := 0; i < k; i++ { // 遍历排序列表的前k个结果
		sub := rankedSubmissions[i]                            // 获取当前位置的简历
		relevanceScore := getRelevanceScore(sub.RelevanceTier) // 获取该简历的相关性分数

		if relevanceScore >= 2.0 { // 如果相关性分数大于等于2.0 (即Tier1或Tier2)，则视为命中
			hits++                   // 命中数加一
			if !firstRelevantFound { // 如果这是第一次找到相关结果
				mrr = 1.0 / float64(i+1)  // 计算MRR，其值为第一个相关结果排名的倒数
				firstRelevantFound = true // 将标志位设为true
			}
		}
		dcg += relevanceScore / math.Log2(float64(i+2)) // 累加DCG值，分母使用i+2以确保对数计算有意义
	}

	// 计算理想DCG(IDCG)
	var idealRelevances []float64 // 创建一个切片来存储理想情况下的相关性分数列表
	// 构建理想相关性分数列表
	for range goldenSet.Tier1 { // 遍历黄金集中的Tier1
		idealRelevances = append(idealRelevances, getRelevanceScore("Tier1")) // 添加Tier1的分数
	}
	for range goldenSet.Tier2 { // 遍历黄金集中的Tier2
		idealRelevances = append(idealRelevances, getRelevanceScore("Tier2")) // 添加Tier2的分数
	}
	for range goldenSet.Tier3 { // 遍历黄金集中的Tier3
		idealRelevances = append(idealRelevances, getRelevanceScore("Tier3")) // 添加Tier3的分数
	}
	// 按相关性分数降序排序
	sort.Slice(idealRelevances, func(i, j int) bool { // 对理想分数列表进行降序排序
		return idealRelevances[i] > idealRelevances[j] // 排序规则
	})

	// 计算IDCG值
	var idcg float64              // 初始化IDCG（理想折损累积增益）
	if k > len(idealRelevances) { // 如果k值大于理想列表的长度
		k = len(idealRelevances) // 则将k设置为理想列表的长度
	}
	for i := 0; i < k; i++ { // 遍历理想列表的前k个结果
		idcg += idealRelevances[i] / math.Log2(float64(i+2)) // 累加计算IDCG
	}

	// 计算NDCG
	var ndcg float64 // 初始化NDCG（归一化折损累积增益）
	if idcg > 0 {    // 只有在IDCG大于0时计算才有意义
		ndcg = dcg / idcg // NDCG = DCG / IDCG
	}

	// 返回所有计算的评估指标
	return PerformanceMetrics{
		Precision: float64(hits) / float64(k),             // 准确率 = 命中的相关结果数 / 返回的总结果数k
		Recall:    float64(hits) / float64(totalRelevant), // 召回率 = 命中的相关结果数 / 所有相关结果的总数
		NDCG:      ndcg,                                   // 归一化折损累积增益
		MRR:       mrr,                                    // 平均倒数排名
	}
}

// TestBestChunkPipeline 实现了一个最小可行流程：召回->按简历选最佳Chunk->取Top-N简历->Rerank
func TestBestChunkPipeline(t *testing.T) { // 定义一个名为 TestBestChunkPipeline 的测试函数
	// 1. 初始化环境 (与 TestRerankerEffectiveness 类似)
	testLogger, err := NewTestLogger(t, "best_chunk_pipeline_analysis") // 创建一个新的测试日志记录器，专门用于此流程的分析
	require.NoError(t, err, "创建测试日志记录器失败")                              // 断言日志记录器创建成功，否则测试失败
	defer testLogger.Close()                                            // 确保在测试结束时关闭日志记录器以释放资源

	testLogger.Log("开始测试 'Best Chunk' 最小可行性流程...") // 记录一条日志，标志着测试的开始
	ctx := context.Background()                    // 创建一个空的背景上下文

	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath) // 从指定的配置文件路径和环境变量加载配置
	require.NoError(t, err, "加载配置失败")                                    // 断言配置加载成功，否则测试失败

	s, err := storage.NewStorage(ctx, cfg) // 根据加载的配置和上下文初始化存储服务
	require.NoError(t, err, "初始化存储组件失败")   // 断言存储服务初始化成功
	defer s.Close()                        // 确保在测试结束时关闭存储服务的连接

	if os.Getenv("CI") != "" { // 检查是否在CI/CD环境中运行
		t.Skip("在CI环境中跳过此测试") // 如果是，则跳过此测试以避免不必要的资源消耗或依赖问题
	}
	if s.Qdrant == nil || cfg.Reranker.URL == "" { // 检查Qdrant向量数据库和Reranker服务是否都已配置
		t.Skip("跳过测试：Qdrant或Reranker服务未配置") // 如果有任何一个未配置，则跳过测试
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding) // 初始化阿里云的文本嵌入器(Embedder)
	require.NoError(t, err, "初始化embedder失败")                                           // 断言Embedder初始化成功

	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。` // 定义一个用于搜索的职位描述(JD)字符串
	vectors, err := embedder.EmbedStrings(ctx, []string{jobDesc})                                                    // 使用Embedder将JD文本转换为向量表示
	require.NoError(t, err)                                                                                          // 断言向量化过程没有错误
	vector := vectors[0]                                                                                             // 获取生成的向量

	goldenSet := getGoBackendGoldenTruthSet()                                                                                  // 获取用于评估的Go后端工程师简历的"黄金标准"集合
	testLogger.Log("黄金评测集已加载: Tier1=%d, Tier2=%d, Tier3=%d", len(goldenSet.Tier1), len(goldenSet.Tier2), len(goldenSet.Tier3)) // 记录黄金集中不同等级的简历数量

	// 2. 步骤 1: 召回 300 个 chunk
	const recallLimit = 300                                                            // 定义向量召回的文档数量上限为300
	retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil) // 在Qdrant中执行相似性搜索，召回300个文档块
	require.NoError(t, err, "向量搜索失败")                                                  // 断言向量搜索成功
	testLogger.Log("步骤 1: 成功召回 %d 个文档块", len(retrievedDocs))                           // 记录召回的文档块数量

	// 3. 步骤 2: 按 uuid 分组，取每组 vector_score 最大的那个 chunk
	bestPerResume := make(map[string]storage.SearchResult) // 创建一个map来存储每个独立简历(uuid)的得分最高的文档块
	for _, doc := range retrievedDocs {                    // 遍历所有召回的文档块
		uuid, ok := doc.Payload["submission_uuid"].(string) // 从Payload中获取简历的唯一标识符(uuid)
		if !ok {                                            // 如果payload中没有uuid
			continue // 跳过这个可能存在的脏数据
		}
		if existing, ok := bestPerResume[uuid]; !ok || doc.Score > existing.Score { // 如果该简历尚未记录，或者当前文档块的向量分数更高
			bestPerResume[uuid] = doc // 更新该简历的最佳代表块
		}
	}
	testLogger.Log("步骤 2: 从召回结果中识别出 %d 份独立简历，并选出其最佳chunk", len(bestPerResume)) // 记录聚合后的独立简历数量

	// 4. 步骤 3: 取 vector_score 前 100 的简历 (的最佳chunk)
	tops := make([]storage.SearchResult, 0, len(bestPerResume)) // 创建一个切片来存储这些最佳文档块
	for _, doc := range bestPerResume {                         // 遍历map中的所有最佳块
		tops = append(tops, doc) // 将最佳块添加到切片中
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].Score > tops[j].Score }) // 按向量分数对切片进行降序排序

	const rerankLimit = 100      // 定义用于rerank的简历数量上限
	if len(tops) > rerankLimit { // 如果筛选出的简历数量超过上限
		tops = tops[:rerankLimit] // 则截取分数最高的前100个
	}
	testLogger.Log("步骤 3: 筛选出 Top %d 的简历用于Rerank", len(tops)) // 记录最终用于Rerank的简历数量

	// 5. 步骤 4: 送入 Reranker
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, tops, t, testLogger) // 调用Reranker服务对这批最佳块进行重排序
	require.NoError(t, err, "调用Reranker服务失败")                                              // 断言Reranker调用成功
	testLogger.Log("步骤 4: Reranker处理完成，返回 %d 个分数", len(rerankScores))                      // 记录Reranker返回的分数数量

	// 步骤 5: 将rerank后的分数赋给简历，并创建用于评估的列表
	// 因为 `tops` 已经确保每个简历只出现一次，所以可以直接转换
	var rankedSubmissions []RankedSubmission // 创建一个用于最终评估的列表
	for _, doc := range tops {               // 遍历用于rerank的top chunk列表
		rerankScore, ok := rerankScores[doc.ID] // 尝试获取该chunk的rerank分数
		if !ok {                                // 如果Reranker没有返回该chunk的分数
			// 如果Reranker没有返回分数，则保留其原始向量分数
			rerankScore = doc.Score // 使用其原始的向量分数作为备用
		}
		if uuid, ok := doc.Payload["submission_uuid"].(string); ok { // 再次获取简历的uuid
			rankedSubmissions = append(rankedSubmissions, RankedSubmission{ // 将结果添加到评估列表中
				UUID:  uuid,        // 简历的uuid
				Score: rerankScore, // 使用的分数（优先用rerank分数，否则用向量分数）
			})
		}
	}

	// 按 rerank 后的分数重新排序
	sort.Slice(rankedSubmissions, func(i, j int) bool { // 对最终的评估列表进行排序
		return rankedSubmissions[i].Score > rankedSubmissions[j].Score // 按最终分数（rerank或vector）进行降序排列
	})

	// 步骤 6: 评估结果
	evaluateAndLogMetrics(t, testLogger, "Best Chunk Pipeline", rankedSubmissions, &goldenSet) // 调用评估函数，并记录该流程的性能指标

	t.Log("'Best Chunk' 最小可行性流程测试完成。") // 在Go测试日志中记录测试完成的消息
}
