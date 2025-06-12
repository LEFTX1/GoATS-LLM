package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"testing"

	"ai-agent-go/internal/config"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/storage"

	"github.com/stretchr/testify/require"
)

// TestRerankerEffectiveness 专门用于评估Reranker模型对搜索结果的实际影响。
// 它会记录每个文档块在Rerank前后的分数，以便进行直接对比。
/*
 * 完整流程:
 * 召回300  → 轻量粗排120 (单批)  → 60×2 分批精排
 * →  批内 z-score  →  RRF 融合  →  简历级(uuid)聚合  → 评测
 */
func TestRerankerEffectiveness(t *testing.T) {
	// ---------- 0. 通用初始化 ----------
	testLogger, err := NewTestLogger(t, "reranker_effectiveness_analysis")
	require.NoError(t, err, "创建测试日志记录器失败")
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

type fusedDoc struct {
	uuid        string
	vectorScore float32
	rerankScore float32
}

// linearFusion 对一组包含两种分数的文档进行归一化和线性融合
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
func evaluateAndLogMetrics(t *testing.T, testLogger *TestLogger, scenarioName string, rankedSubmissions []RankedSubmission, goldenSet *GoldenTruthSet) {
	testLogger.Log("开始为场景 [%s] 计算性能指标...", scenarioName)

	if len(rankedSubmissions) == 0 {
		testLogger.Log("警告: 场景 [%s] 的排名结果为空，跳过指标计算。", scenarioName)
		return
	}

	// 为每个结果分配其在黄金集中的相关性等级
	hasHit := false
	for i := range rankedSubmissions {
		uuid := rankedSubmissions[i].UUID
		if goldenSet.Tier1[uuid] {
			rankedSubmissions[i].RelevanceTier = "Tier1"
			hasHit = true
		} else if goldenSet.Tier2[uuid] {
			rankedSubmissions[i].RelevanceTier = "Tier2"
			hasHit = true
		} else if goldenSet.Tier3[uuid] {
			rankedSubmissions[i].RelevanceTier = "Tier3"
			hasHit = true
		} else {
			rankedSubmissions[i].RelevanceTier = "N/A"
		}
	}

	if !hasHit {
		testLogger.Log("警告: 在场景 [%s] 中，排名结果未命中任何黄金集条目。性能指标可能无意义。", scenarioName)
	}

	// 打印 Top-10 结果以供快速检查
	topN := 10
	if len(rankedSubmissions) < topN {
		topN = len(rankedSubmissions)
	}
	testLogger.LogObject(fmt.Sprintf("[%s] Top %d ranked resumes", scenarioName, topN), rankedSubmissions[:topN])

	// 计算并记录性能指标
	metrics10 := calculatePerformanceMetrics(rankedSubmissions, goldenSet, 10)
	testLogger.Log("[%s] Performance Metrics @10: Precision=%.4f, Recall=%.4f, NDCG=%.4f, MRR=%.4f",
		scenarioName, metrics10.Precision, metrics10.Recall, metrics10.NDCG, metrics10.MRR)

	metrics30 := calculatePerformanceMetrics(rankedSubmissions, goldenSet, 30)
	testLogger.Log("[%s] Performance Metrics @30: Precision=%.4f, Recall=%.4f, NDCG=%.4f",
		scenarioName, metrics30.Precision, metrics30.Recall, metrics30.NDCG)
}

// callRerankerWithDocuments 是一个本地实现的 reranker 调用函数
func callRerankerWithDocuments(ctx context.Context, rerankerURL string, query string, documents []RerankDocument, t *testing.T, logger *TestLogger) (map[string]float32, error) {
	if rerankerURL == "" {
		logger.Log("Reranker URL is not configured. Skipping rerank.")
		return make(map[string]float32), nil
	}

	reqBody := RerankRequest{
		Query:     query,
		Documents: documents,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal rerank request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", rerankerURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call reranker service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("reranker service returned non-OK status: %s, body: %s", resp.Status, string(bodyBytes))
	}

	var rerankedDocs []RerankedDocument
	if err := json.NewDecoder(resp.Body).Decode(&rerankedDocs); err != nil {
		return nil, fmt.Errorf("failed to decode reranker response: %w", err)
	}

	scores := make(map[string]float32)
	for _, doc := range rerankedDocs {
		scores[doc.ID] = doc.RerankScore
	}

	return scores, nil
}

// --- 评估指标相关辅助函数 ---

// RankedSubmission 用于评估的排序后简历对象
type RankedSubmission struct {
	UUID          string
	Score         float32
	RelevanceTier string
}

// PerformanceMetrics 存储计算出的评估指标
type PerformanceMetrics struct {
	Precision float64
	Recall    float64
	NDCG      float64
	MRR       float64
}

// getRelevanceScore 将黄金集中的Tier映射为数值分数用于NDCG计算
func getRelevanceScore(tier string) float64 {
	switch tier {
	case "Tier1":
		return 3.0
	case "Tier2":
		return 2.0
	case "Tier3":
		return 1.0
	default:
		return 0.0
	}
}

// calculatePerformanceMetrics 计算各种排序评估指标
func calculatePerformanceMetrics(rankedSubmissions []RankedSubmission, goldenSet *GoldenTruthSet, k int) PerformanceMetrics {
	var hits, totalRelevant int
	var dcg, mrr float64
	firstRelevantFound := false

	totalRelevant = len(goldenSet.Tier1) + len(goldenSet.Tier2) // 假定Tier1和Tier2是相关的
	if totalRelevant == 0 {
		totalRelevant = 1 // Avoid division by zero for recall
	}

	if k > len(rankedSubmissions) {
		k = len(rankedSubmissions)
	}

	for i := 0; i < k; i++ {
		sub := rankedSubmissions[i]
		relevanceScore := getRelevanceScore(sub.RelevanceTier)

		if relevanceScore >= 2.0 { // Tier1 或 Tier2
			hits++
			if !firstRelevantFound {
				mrr = 1.0 / float64(i+1)
				firstRelevantFound = true
			}
		}
		dcg += relevanceScore / math.Log2(float64(i+2))
	}

	// 计算IDCG
	var idealRelevances []float64
	for range goldenSet.Tier1 {
		idealRelevances = append(idealRelevances, getRelevanceScore("Tier1"))
	}
	for range goldenSet.Tier2 {
		idealRelevances = append(idealRelevances, getRelevanceScore("Tier2"))
	}
	for range goldenSet.Tier3 {
		idealRelevances = append(idealRelevances, getRelevanceScore("Tier3"))
	}
	sort.Slice(idealRelevances, func(i, j int) bool {
		return idealRelevances[i] > idealRelevances[j]
	})

	var idcg float64
	if k > len(idealRelevances) {
		k = len(idealRelevances)
	}
	for i := 0; i < k; i++ {
		idcg += idealRelevances[i] / math.Log2(float64(i+2))
	}

	var ndcg float64
	if idcg > 0 {
		ndcg = dcg / idcg
	}

	return PerformanceMetrics{
		Precision: float64(hits) / float64(k),
		Recall:    float64(hits) / float64(totalRelevant),
		NDCG:      ndcg,
		MRR:       mrr,
	}
}

// TestBestChunkPipeline 实现了一个最小可行流程：召回->按简历选最佳Chunk->取Top-N简历->Rerank
func TestBestChunkPipeline(t *testing.T) {
	// 1. 初始化环境 (与 TestRerankerEffectiveness 类似)
	testLogger, err := NewTestLogger(t, "best_chunk_pipeline_analysis")
	require.NoError(t, err, "创建测试日志记录器失败")
	defer testLogger.Close()

	testLogger.Log("开始测试 'Best Chunk' 最小可行性流程...")
	ctx := context.Background()

	cfg, err := config.LoadConfigFromFileAndEnv(jobSearchTestConfigPath)
	require.NoError(t, err, "加载配置失败")

	s, err := storage.NewStorage(ctx, cfg)
	require.NoError(t, err, "初始化存储组件失败")
	defer s.Close()

	if os.Getenv("CI") != "" {
		t.Skip("在CI环境中跳过此测试")
	}
	if s.Qdrant == nil || cfg.Reranker.URL == "" {
		t.Skip("跳过测试：Qdrant或Reranker服务未配置")
	}

	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, cfg.Aliyun.Embedding)
	require.NoError(t, err, "初始化embedder失败")

	jobDesc := `我们正在寻找一位经验丰富的高级Go后端及微服务工程师，加入我们的核心技术团队，负责设计、开发和维护大规模、高可用的分布式系统。您将有机会参与到从架构设计到服务上线的全过程，应对高并发、低延迟的挑战。`
	vectors, err := embedder.EmbedStrings(ctx, []string{jobDesc})
	require.NoError(t, err)
	vector := vectors[0]

	goldenSet := getGoBackendGoldenTruthSet()
	testLogger.Log("黄金评测集已加载: Tier1=%d, Tier2=%d, Tier3=%d", len(goldenSet.Tier1), len(goldenSet.Tier2), len(goldenSet.Tier3))

	// 2. 步骤 1: 召回 300 个 chunk
	const recallLimit = 300
	retrievedDocs, err := s.Qdrant.SearchSimilarResumes(ctx, vector, recallLimit, nil)
	require.NoError(t, err, "向量搜索失败")
	testLogger.Log("步骤 1: 成功召回 %d 个文档块", len(retrievedDocs))

	// 3. 步骤 2: 按 uuid 分组，取每组 vector_score 最大的那个 chunk
	bestPerResume := make(map[string]storage.SearchResult)
	for _, doc := range retrievedDocs {
		uuid, ok := doc.Payload["submission_uuid"].(string)
		if !ok {
			continue // 跳过没有uuid的脏数据
		}
		if existing, ok := bestPerResume[uuid]; !ok || doc.Score > existing.Score {
			bestPerResume[uuid] = doc
		}
	}
	testLogger.Log("步骤 2: 从召回结果中识别出 %d 份独立简历，并选出其最佳chunk", len(bestPerResume))

	// 4. 步骤 3: 取 vector_score 前 100 的简历 (的最佳chunk)
	tops := make([]storage.SearchResult, 0, len(bestPerResume))
	for _, doc := range bestPerResume {
		tops = append(tops, doc)
	}
	sort.Slice(tops, func(i, j int) bool { return tops[i].Score > tops[j].Score })

	const rerankLimit = 100
	if len(tops) > rerankLimit {
		tops = tops[:rerankLimit]
	}
	testLogger.Log("步骤 3: 筛选出 Top %d 的简历用于Rerank", len(tops))

	// 5. 步骤 4: 送入 Reranker
	rerankScores, err := callReranker(ctx, cfg.Reranker.URL, jobDesc, tops, t, testLogger)
	require.NoError(t, err, "调用Reranker服务失败")
	testLogger.Log("步骤 4: Reranker处理完成，返回 %d 个分数", len(rerankScores))

	// 步骤 5: 将rerank后的分数赋给简历，并创建用于评估的列表
	// 因为 `tops` 已经确保每个简历只出现一次，所以可以直接转换
	var rankedSubmissions []RankedSubmission
	for _, doc := range tops {
		rerankScore, ok := rerankScores[doc.ID]
		if !ok {
			// 如果Reranker没有返回分数，则保留其原始向量分数
			rerankScore = doc.Score
		}
		if uuid, ok := doc.Payload["submission_uuid"].(string); ok {
			rankedSubmissions = append(rankedSubmissions, RankedSubmission{
				UUID:  uuid,
				Score: rerankScore,
			})
		}
	}

	// 按 rerank 后的分数重新排序
	sort.Slice(rankedSubmissions, func(i, j int) bool {
		return rankedSubmissions[i].Score > rankedSubmissions[j].Score
	})

	// 步骤 6: 评估结果
	evaluateAndLogMetrics(t, testLogger, "Best Chunk Pipeline", rankedSubmissions, &goldenSet)

	t.Log("'Best Chunk' 最小可行性流程测试完成。")
}
