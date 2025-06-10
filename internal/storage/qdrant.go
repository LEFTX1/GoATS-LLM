package storage

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/tracing"
	"ai-agent-go/internal/types"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gofrs/uuid/v5" // 导入uuid包
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// 定义Qdrant的专用tracer
var qdrantTracer = otel.Tracer("ai-agent-go/storage/qdrant")

// QdrantPointIDNamespace is a dedicated namespace for generating deterministic Qdrant point IDs for resume chunks.
// This ensures that for the same resume and same chunk, we always get the same point ID.
// UUID generated via `uuidgen`
var QdrantPointIDNamespace = uuid.Must(uuid.FromString("fd6c72c2-5a33-4b53-8e7c-8298f3f5a7e1"))

// VectorDatabase 向量数据库接口
type VectorDatabase interface {
	// StoreResumeVectors 存储简历向量
	StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float64) ([]string, error)

	// GetVectorsBySubmissionUUID 通过 submission_uuid 获取向量
	GetVectorsBySubmissionUUID(ctx context.Context, submissionUUID string) ([]SearchResult, error)

	// SearchSimilarResumes 搜索相似简历
	SearchSimilarResumes(ctx context.Context, queryVector []float64, limit int, filter map[string]interface{}) ([]SearchResult, error)
}

// 确保Qdrant实现了VectorDatabase接口
var _ VectorDatabase = (*Qdrant)(nil)

// Qdrant 提供向量数据库功能
type Qdrant struct {
	endpoint       string
	collectionName string
	vectorSize     int
	distanceMetric string
	httpClient     *http.Client
}

// SearchResult 表示一个搜索结果项
type SearchResult struct {
	ID      string                 // 向量ID
	Score   float32                // 相似度分数
	Payload map[string]interface{} // 载荷数据
}

// QdrantOption 定义Qdrant构造函数选项
type QdrantOption func(*Qdrant)

// WithDistanceMetric 设置距离度量
func WithDistanceMetric(metric string) QdrantOption {
	return func(q *Qdrant) {
		q.distanceMetric = metric
	}
}

// WithHttpTimeout 设置HTTP客户端超时
func WithHttpTimeout(timeout time.Duration) QdrantOption {
	return func(q *Qdrant) {
		q.httpClient = &http.Client{Timeout: timeout}
	}
}

// NewQdrant 创建Qdrant客户端
func NewQdrant(cfg *config.QdrantConfig, opts ...QdrantOption) (*Qdrant, error) {
	if cfg == nil {
		return nil, fmt.Errorf("qdrant配置不能为空")
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:6333" // 默认端点
	}

	collectionName := cfg.Collection
	if collectionName == "" {
		collectionName = "resume_chunks" // 默认集合名
	}

	vectorSize := cfg.Dimension
	if vectorSize <= 0 {
		vectorSize = 1024 // 默认向量维度，与阿里云Embedding一致
	}

	q := &Qdrant{
		endpoint:       endpoint,
		collectionName: collectionName,
		vectorSize:     vectorSize,
		distanceMetric: "Cosine", // 使用余弦相似度
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}

	// 应用选项
	for _, opt := range opts {
		opt(q)
	}

	// 确保集合存在
	if err := q.ensureCollectionExists(context.Background()); err != nil {
		return nil, fmt.Errorf("确保集合 '%s' 存在失败: %w", collectionName, err)
	}

	log.Printf("成功连接到Qdrant服务器: %s，并确保集合 '%s' 存在", endpoint, collectionName)
	return q, nil
}

// ensureCollectionExists 确保向量集合存在
func (q *Qdrant) ensureCollectionExists(ctx context.Context) error {
	// 创建一个命名span
	ctx, span := qdrantTracer.Start(ctx, "Qdrant.EnsureCollectionExists",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加基础属性
	span.SetAttributes(
		attribute.String("net.peer.name", q.endpoint),
		attribute.String("db.system", "qdrant"),
		attribute.String("db.operation", "check_collection"),
		attribute.String("db.collection", q.collectionName),
		attribute.Int("db.vector_size", q.vectorSize),
	)

	// 先检查集合是否已存在
	url := fmt.Sprintf("%s/collections/%s", q.endpoint, q.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("创建检查集合请求失败: %w", err)
	}

	// 注入OpenTelemetry追踪上下文到HTTP请求
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	span.SetAttributes(
		attribute.String("http.method", http.MethodGet),
		attribute.String("http.url", url),
	)

	resp, err := q.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("发送检查集合请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 如果集合不存在，则创建它
	if resp.StatusCode == http.StatusNotFound {
		span.AddEvent("collection_not_found", trace.WithAttributes(
			attribute.String("action", "create_collection"),
		))
		log.Printf("集合 '%s' 不存在，将创建新集合", q.collectionName)
		return q.createCollection(ctx)
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("检查集合失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	// 检查集合配置是否匹配当前配置
	var collectionInfo struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors struct {
						Size     int    `json:"size"`
						Distance string `json:"distance"`
					} `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
		} `json:"result"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("读取集合信息响应失败: %w", err)
	}

	if err := json.Unmarshal(body, &collectionInfo); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("解析集合信息失败: %w", err)
	}

	existingSize := collectionInfo.Result.Config.Params.Vectors.Size
	existingDistance := collectionInfo.Result.Config.Params.Vectors.Distance

	span.SetAttributes(
		attribute.Int("collection.existing_vector_size", existingSize),
		attribute.String("collection.existing_distance", existingDistance),
	)

	if existingSize != q.vectorSize || existingDistance != q.distanceMetric {
		log.Printf("警告: 现有集合配置与当前配置不匹配。现有: 维度=%d, 距离=%s; 当前: 维度=%d, 距离=%s",
			existingSize, existingDistance, q.vectorSize, q.distanceMetric)
		// 如需重新创建集合，可在此处添加逻辑

		span.AddEvent("collection_config_mismatch", trace.WithAttributes(
			attribute.Int("expected_vector_size", q.vectorSize),
			attribute.String("expected_distance", q.distanceMetric),
		))
	}

	span.SetStatus(codes.Ok, "")
	log.Printf("已发现现有Qdrant集合: %s，维度: %d", q.collectionName, existingSize)
	return nil
}

// createCollection 创建新的向量集合
func (q *Qdrant) createCollection(ctx context.Context) error {
	// 创建一个命名span
	ctx, span := qdrantTracer.Start(ctx, "Qdrant.CreateCollection",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加基础属性
	span.SetAttributes(
		attribute.String("net.peer.name", q.endpoint),
		attribute.String("db.system", "qdrant"),
		attribute.String("db.operation", "create_collection"),
		attribute.String("db.collection", q.collectionName),
		attribute.Int("db.vector_size", q.vectorSize),
		attribute.String("db.vector.distance", q.distanceMetric),
	)

	// 准备创建集合的请求
	createReqBody := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     q.vectorSize,
			"distance": q.distanceMetric,
		},
		// 可添加索引配置等其他参数
		"optimizers_config": map[string]interface{}{
			"default_segment_number": 2,
		},
	}

	jsonData, err := json.Marshal(createReqBody)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("序列化创建集合请求失败: %w", err)
	}

	// 发送创建集合请求
	url := fmt.Sprintf("%s/collections/%s", q.endpoint, q.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(jsonData))
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("创建集合请求对象失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	// 注入OpenTelemetry追踪上下文到HTTP请求
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	span.SetAttributes(
		attribute.String("http.method", http.MethodPut),
		attribute.String("http.url", url),
		attribute.Int("http.request_content_length", len(jsonData)),
	)

	resp, err := q.httpClient.Do(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("发送创建集合请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		err := fmt.Errorf("创建集合失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	span.SetStatus(codes.Ok, "")
	log.Printf("已成功创建Qdrant集合: %s，维度: %d", q.collectionName, q.vectorSize)
	return nil
}

// StoreResumeVectors 存储简历向量
func (q *Qdrant) StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float64) ([]string, error) {
	// 创建一个命名span
	ctx, span := qdrantTracer.Start(ctx, "Qdrant.StoreResumeVectors",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加基础属性
	span.SetAttributes(
		attribute.String("db.system", "qdrant"),
		attribute.String("db.operation", "store_vectors"),
		attribute.String("db.collection", q.collectionName),
		attribute.String("resume.id", resumeID),
		attribute.Int("vectors.count", len(embeddings)),
	)

	// 验证输入
	if len(chunks) != len(embeddings) {
		err := fmt.Errorf("chunks数量(%d)与embeddings数量(%d)不匹配", len(chunks), len(embeddings))
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	if len(embeddings) == 0 {
		span.AddEvent("warning", trace.WithAttributes(
			attribute.String("message", "没有要存储的向量"),
		))
		span.SetStatus(codes.Ok, "no vectors to store")
		return []string{}, nil
	}

	// 准备要存储的点
	points := make([]interface{}, 0, len(embeddings))
	ids := make([]string, 0, len(embeddings))

	// 遍历所有chunk和embedding
	for i, embedding := range embeddings {
		if len(embedding) != q.vectorSize {
			err := fmt.Errorf("向量维度(%d)与配置维度(%d)不匹配", len(embedding), q.vectorSize)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil, err
		}

		chunk := chunks[i]
		// 生成一个确定性的唯一ID，基于 resumeID 和 chunk ID
		// 使用 resumeID 和 chunk 的唯一标识符作为源来保证幂等性
		idSource := fmt.Sprintf("resume_id:%s_chunk_id:%d", resumeID, chunk.ChunkID)
		pointID := uuid.NewV5(QdrantPointIDNamespace, idSource).String()
		ids = append(ids, pointID)

		// 准备payload，包含所有需要存储的元数据
		payload := map[string]interface{}{
			"submission_uuid":  resumeID, // 使用 submission_uuid 作为键
			"chunk_idx":        i,
			"chunk_id":         chunk.ChunkID,
			"content":          tracing.SafeResumeContent(chunk.Content), // 安全处理简历内容
			"chunk_type":       chunk.ChunkType,
			"chunk_title":      chunk.ChunkTitle, // 使用Section的Title作为标题
			"importance_score": chunk.ImportanceScore,
		}

		// 添加唯一标识符 - 使用安全处理防止PII泄露
		if chunk.UniqueIdentifiers.Name != "" {
			payload["name"] = tracing.MaskPII(chunk.UniqueIdentifiers.Name)
		}
		if chunk.UniqueIdentifiers.Email != "" {
			payload["email"] = tracing.MaskPII(chunk.UniqueIdentifiers.Email)
		}
		if chunk.UniqueIdentifiers.Phone != "" {
			payload["phone"] = tracing.MaskPII(chunk.UniqueIdentifiers.Phone)
		}

		// 添加元数据
		if chunk.Metadata.ExperienceYears > 0 {
			payload["experience_years"] = chunk.Metadata.ExperienceYears
		}
		if chunk.Metadata.EducationLevel != "" {
			payload["education_level"] = chunk.Metadata.EducationLevel
		}

		// 创建向量点
		point := map[string]interface{}{
			"id":      pointID, // Qdrant 要求 ID 是 UUID 或 uint64
			"vector":  embedding,
			"payload": payload,
		}

		points = append(points, point)
	}

	// 在span中记录首个chunk的简要信息 (安全处理)
	if len(chunks) > 0 {
		firstChunk := chunks[0]
		span.SetAttributes(
			attribute.String("sample.chunk_type", firstChunk.ChunkType),
			attribute.Int("sample.importance_score", int(firstChunk.ImportanceScore*100)), // 转为整数
			attribute.String("sample.content_preview", tracing.SafeResumeContent(firstChunk.Content)),
		)
	}

	// 构造请求体
	reqBody := map[string]interface{}{
		"points": points,
	}

	// 执行API调用
	var result struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
		Status string  `json:"status"`
		Time   float64 `json:"time"`
	}

	// 使用doRequest方法发送请求
	err := q.doRequest(ctx, "PUT", fmt.Sprintf("/collections/%s/points?wait=true", q.collectionName), reqBody, &result)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	// 记录API响应信息
	span.SetAttributes(
		attribute.String("qdrant.response_status", result.Status),
		attribute.Float64("qdrant.response_time", result.Time),
	)

	span.SetStatus(codes.Ok, "")
	return ids, nil
}

// SearchSimilarResumes 在Qdrant中搜索与给定查询向量相似的简历
func (q *Qdrant) SearchSimilarResumes(ctx context.Context, queryVector []float64, limit int, filter map[string]interface{}) ([]SearchResult, error) {
	// 创建一个命名span
	ctx, span := qdrantTracer.Start(ctx, "Qdrant.SearchSimilarResumes",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加基础属性
	span.SetAttributes(
		attribute.String("db.system", "qdrant"),
		attribute.String("db.operation", "search_vectors"),
		attribute.String("db.collection", q.collectionName),
		attribute.Int("search.limit", limit),
		attribute.String("search.filter", fmt.Sprintf("%v", filter)),
		attribute.Int("query_vector.size", len(queryVector)),
	)

	if len(queryVector) != q.vectorSize {
		err := fmt.Errorf("查询向量维度(%d)与配置维度(%d)不匹配", len(queryVector), q.vectorSize)
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	if limit <= 0 {
		limit = 10 // 默认限制为10
	}

	// 构造请求体
	searchReq := map[string]interface{}{
		"vector":       queryVector,
		"limit":        limit,
		"with_payload": true,
	}

	// 如果有过滤器，添加到请求中
	if filter != nil && len(filter) > 0 {
		// Qdrant要求过滤器具有特定格式，这里简化处理
		searchReq["filter"] = filter
	}

	// 发送搜索请求
	var result struct {
		Result []struct {
			ID      string                 `json:"id"`
			Score   float32                `json:"score"`
			Payload map[string]interface{} `json:"payload"`
		} `json:"result"`
		Status string  `json:"status"`
		Time   float64 `json:"time"`
	}

	// 使用doRequest方法发送请求
	err := q.doRequest(ctx, "POST", fmt.Sprintf("/collections/%s/points/search", q.collectionName), searchReq, &result)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	// 转换结果格式
	searchResults := make([]SearchResult, 0, len(result.Result))
	for _, point := range result.Result {
		searchResults = append(searchResults, SearchResult{
			ID:      point.ID,
			Score:   point.Score,
			Payload: point.Payload,
		})
	}

	// 记录搜索统计信息
	span.SetAttributes(
		attribute.Int("search.results.count", len(searchResults)),
		attribute.String("qdrant.response_status", result.Status),
		attribute.Float64("qdrant.response_time", result.Time),
	)

	span.SetStatus(codes.Ok, "")
	return searchResults, nil
}

// DeletePoints 删除指定ID的向量点
func (q *Qdrant) DeletePoints(ctx context.Context, pointIDs []string) error {
	// 创建一个命名span
	ctx, span := qdrantTracer.Start(ctx, "Qdrant.DeletePoints",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加基础属性
	span.SetAttributes(
		attribute.String("db.system", "qdrant"),
		attribute.String("db.operation", "delete_points"),
		attribute.String("db.collection", q.collectionName),
		attribute.Int("points.count", len(pointIDs)),
	)

	if len(pointIDs) == 0 {
		span.SetStatus(codes.Ok, "no points to delete")
		return nil
	}

	// 构造请求体
	reqBody := map[string]interface{}{
		"points": pointIDs,
	}

	// 执行API调用
	var result struct {
		Result struct {
			Status string `json:"status"`
		} `json:"result"`
		Status string  `json:"status"`
		Time   float64 `json:"time"`
	}

	// 使用doRequest方法发送请求
	err := q.doRequest(ctx, "POST", fmt.Sprintf("/collections/%s/points/delete?wait=true", q.collectionName), reqBody, &result)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	// 记录API响应信息
	span.SetAttributes(
		attribute.String("qdrant.response_status", result.Status),
		attribute.Float64("qdrant.response_time", result.Time),
	)

	span.SetStatus(codes.Ok, "")
	return nil
}

// 辅助函数 - 截断字符串
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen > 3 {
		return s[:maxLen-3] + "..."
	}
	return s[:maxLen]
}

// 定义通用错误
var (
	ErrVectorDBNotConfigured = fmt.Errorf("vector database not configured")
)

// ResumeVectorStore 提供简历向量存储功能
type ResumeVectorStore struct {
	VectorDB VectorDatabase
}

// NewResumeVectorStore 创建一个新的简历向量存储
func NewResumeVectorStore(vectorDB VectorDatabase) *ResumeVectorStore {
	return &ResumeVectorStore{
		VectorDB: vectorDB,
	}
}

// StoreResumeVectors 存储简历向量
func (s *ResumeVectorStore) StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float64) ([]string, error) {
	if s.VectorDB == nil {
		return nil, ErrVectorDBNotConfigured
	}
	return s.VectorDB.StoreResumeVectors(ctx, resumeID, chunks, embeddings)
}

// SearchSimilarResumes 搜索相似简历
func (s *ResumeVectorStore) SearchSimilarResumes(ctx context.Context, queryVector []float64, limit int, filter map[string]interface{}) ([]SearchResult, error) {
	if s.VectorDB == nil {
		return nil, ErrVectorDBNotConfigured
	}
	return s.VectorDB.SearchSimilarResumes(ctx, queryVector, limit, filter)
}

func (q *Qdrant) doRequest(ctx context.Context, method, path string, body interface{}, result interface{}) error {
	// 创建请求和span
	ctx, span := qdrantTracer.Start(ctx, fmt.Sprintf("%s %s", method, path),
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 设置span属性
	span.SetAttributes(
		attribute.String("net.peer.name", q.endpoint),
		attribute.String("db.system", "qdrant"),
		attribute.String("db.operation", path),
	)

	// 获取BaseURL
	baseURL := q.endpoint

	// 准备请求
	var req *http.Request
	var err error

	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			tracing.RecordError(span, err, tracing.ErrorTypeVectorDB)
			return err
		}

		req, err = http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewBuffer(jsonBody))
		if err != nil {
			tracing.RecordError(span, err, tracing.ErrorTypeVectorDB)
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		span.SetAttributes(attribute.Int("http.request.body.size", len(jsonBody)))
	} else {
		req, err = http.NewRequestWithContext(ctx, method, baseURL+path, nil)
		if err != nil {
			tracing.RecordError(span, err, tracing.ErrorTypeVectorDB)
			return err
		}
	}

	// 注入trace context
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))

	// 执行请求
	resp, err := q.httpClient.Do(req)
	if err != nil {
		tracing.RecordError(span, err, tracing.ErrorTypeHTTP)
		return err
	}
	defer resp.Body.Close()

	// 设置状态码属性
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		tracing.RecordError(span, err, tracing.ErrorTypeHTTP)
		return err
	}

	span.SetAttributes(attribute.Int("http.response.body.size", len(respBody)))

	// 检查状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err = fmt.Errorf("qdrant API error: status=%d, body=%s", resp.StatusCode, string(respBody))
		tracing.RecordHTTPError(span, err, resp.StatusCode)
		return err
	}

	// 解析结果
	if result != nil && len(respBody) > 0 {
		if err = json.Unmarshal(respBody, result); err != nil {
			tracing.RecordError(span, err, tracing.ErrorTypeVectorDB)
			return err
		}
	}

	span.SetStatus(codes.Ok, "")
	return nil
}

// GetVectorsBySubmissionUUID 通过 submission_uuid 获取所有相关的向量点
func (q *Qdrant) GetVectorsBySubmissionUUID(ctx context.Context, submissionUUID string) ([]SearchResult, error) {
	ctx, span := qdrantTracer.Start(ctx, "Qdrant.GetVectorsBySubmissionUUID",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system", "qdrant"),
			attribute.String("db.operation", "scroll"),
			attribute.String("db.collection", q.collectionName),
			attribute.String("submission_uuid", submissionUUID),
		),
	)
	defer span.End()

	scrollReqBody := map[string]interface{}{
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key": "submission_uuid",
					"match": map[string]interface{}{
						"value": submissionUUID,
					},
				},
			},
		},
		"with_payload": true,
		"with_vector":  true, // 同时获取向量数据
		"limit":        100,  // 一个简历通常不会超过100个分块
	}

	var scrollResp struct {
		Result struct {
			Points []struct {
				ID      string                 `json:"id"`
				Payload map[string]interface{} `json:"payload"`
				Vector  []float32              `json:"vector"`
			} `json:"points"`
		} `json:"result"`
		Status string  `json:"status"`
		Time   float64 `json:"time"`
	}

	err := q.doRequest(ctx, http.MethodPost, fmt.Sprintf("/collections/%s/points/scroll", q.collectionName), scrollReqBody, &scrollResp)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to scroll points by submission_uuid")
		return nil, err
	}

	var results []SearchResult
	for _, point := range scrollResp.Result.Points {
		results = append(results, SearchResult{
			ID:      point.ID,
			Score:   0, // Scroll 操作没有分数
			Payload: point.Payload,
			// Vector: point.Vector, // 可根据需要决定是否返回向量本身
		})
	}

	span.SetAttributes(attribute.Int("retrieved_points_count", len(results)))
	span.SetStatus(codes.Ok, "")
	return results, nil
}

// CountPoints 获取集合中的点数量
func (q *Qdrant) CountPoints(ctx context.Context) (int64, error) {
	// 创建一个命名span
	ctx, span := qdrantTracer.Start(ctx, "Qdrant.CountPoints",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	// 添加基础属性
	span.SetAttributes(
		attribute.String("net.peer.name", q.endpoint),
		attribute.String("db.system", "qdrant"),
		attribute.String("db.operation", "count_points"),
		attribute.String("db.collection", q.collectionName),
	)

	// 构建请求
	countReqBody := map[string]interface{}{
		"exact": true, // 精确计数
	}

	// 执行API调用
	var result struct {
		Result struct {
			Count int64 `json:"count"`
		} `json:"result"`
		Status string  `json:"status"`
		Time   float64 `json:"time"`
	}

	// 使用doRequest方法发送请求
	err := q.doRequest(ctx, "POST", fmt.Sprintf("/collections/%s/points/count", q.collectionName), countReqBody, &result)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return 0, err
	}

	// 记录API响应信息
	span.SetAttributes(
		attribute.String("qdrant.response_status", result.Status),
		attribute.Float64("qdrant.response_time", result.Time),
		attribute.Int64("qdrant.points.count", result.Result.Count),
	)

	span.SetStatus(codes.Ok, "")
	return result.Result.Count, nil
}
