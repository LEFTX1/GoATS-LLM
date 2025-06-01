package storage

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/types"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// VectorDatabase 向量数据库接口
type VectorDatabase interface {
	// StoreResumeVectors 存储简历向量
	StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float64) ([]string, error)

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
		return nil, fmt.Errorf("Qdrant配置不能为空")
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
	// 先检查集合是否已存在
	url := fmt.Sprintf("%s/collections/%s", q.endpoint, q.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("创建检查集合请求失败: %w", err)
	}

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送检查集合请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 如果集合不存在，则创建它
	if resp.StatusCode == http.StatusNotFound {
		log.Printf("集合 '%s' 不存在，将创建新集合", q.collectionName)
		return q.createCollection(ctx)
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("检查集合失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
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
		return fmt.Errorf("读取集合信息响应失败: %w", err)
	}

	if err := json.Unmarshal(body, &collectionInfo); err != nil {
		return fmt.Errorf("解析集合信息失败: %w", err)
	}

	existingSize := collectionInfo.Result.Config.Params.Vectors.Size
	existingDistance := collectionInfo.Result.Config.Params.Vectors.Distance

	if existingSize != q.vectorSize || existingDistance != q.distanceMetric {
		log.Printf("警告: 现有集合配置与当前配置不匹配。现有: 维度=%d, 距离=%s; 当前: 维度=%d, 距离=%s",
			existingSize, existingDistance, q.vectorSize, q.distanceMetric)
		// 如需重新创建集合，可在此处添加逻辑
	}

	log.Printf("已发现现有Qdrant集合: %s，维度: %d", q.collectionName, existingSize)
	return nil
}

// createCollection 创建新的向量集合
func (q *Qdrant) createCollection(ctx context.Context) error {
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
		return fmt.Errorf("序列化创建集合请求失败: %w", err)
	}

	// 发送创建集合请求
	url := fmt.Sprintf("%s/collections/%s", q.endpoint, q.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建集合请求对象失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送创建集合请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("创建集合失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	log.Printf("已成功创建Qdrant集合: %s，维度: %d", q.collectionName, q.vectorSize)
	return nil
}

// StoreResumeVectors 存储简历向量
// 现在接受float64类型向量，与阿里云API兼容
func (q *Qdrant) StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float64) ([]string, error) {
	if len(chunks) != len(embeddings) {
		return nil, fmt.Errorf("chunks和embeddings数量不匹配: %d vs %d", len(chunks), len(embeddings))
	}

	if len(chunks) == 0 {
		return []string{}, nil // 没有数据要插入
	}

	// 构建Qdrant的点数据
	points := make([]map[string]interface{}, len(chunks))
	pointIDs := make([]string, len(chunks))

	for i, chunk := range chunks {
		// 为每个chunk生成一个唯一的ID
		pointID := fmt.Sprintf("%s_chunk_%d", resumeID, chunk.ChunkID)
		pointIDs[i] = pointID

		// 构建payload
		payload := map[string]interface{}{
			"resume_id":        resumeID,
			"chunk_id":         chunk.ChunkID,
			"chunk_type":       chunk.ChunkType,
			"content_preview":  truncateString(chunk.Content, 200),
			"importance_score": chunk.ImportanceScore,
		}

		// 添加元数据字段 (Metadata)
		// 注意：如果 ExperienceYears 为 0，我们也希望存储它，以区分"经验为0"和"经验未知/未提供"
		// 因此，不再使用 > 0 的判断，而是检查 basicInfo 中是否存在相关字段并已正确转换为 int
		// 这里的 chunk.Metadata.ExperienceYears 已经是处理过的值
		payload["experience_years"] = chunk.Metadata.ExperienceYears

		if chunk.Metadata.EducationLevel != "" {
			payload["education_level"] = chunk.Metadata.EducationLevel
		}

		// 添加扁平化的 UniqueIdentifiers
		if chunk.UniqueIdentifiers.Name != "" {
			payload["candidate_name"] = chunk.UniqueIdentifiers.Name
		}
		if chunk.UniqueIdentifiers.Phone != "" {
			payload["candidate_phone"] = chunk.UniqueIdentifiers.Phone
		}
		if chunk.UniqueIdentifiers.Email != "" {
			payload["candidate_email"] = chunk.UniqueIdentifiers.Email
		}

		// 创建点 - Qdrant API 接受 float64 或者 float32 (通常自动转换)
		// 我们的 embeddings 是 [][]float64，保持不变
		points[i] = map[string]interface{}{
			"id":      pointID,
			"vector":  embeddings[i],
			"payload": payload,
		}
	}

	// 准备upsert请求
	requestBody := map[string]interface{}{
		"points": points,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("序列化upsert请求失败: %w", err)
	}

	// 发送请求
	url := fmt.Sprintf("%s/collections/%s/points?wait=true", q.endpoint, q.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建upsert请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送upsert请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("更新向量失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	log.Printf("成功存储 %d 个向量点到集合 %s", len(points), q.collectionName)
	return pointIDs, nil
}

// SearchSimilarResumes 搜索相似简历
// 现在接受float64类型向量，与阿里云API兼容
func (q *Qdrant) SearchSimilarResumes(ctx context.Context, queryVector []float64, limit int, filter map[string]interface{}) ([]SearchResult, error) {
	// 构建搜索请求
	searchRequest := map[string]interface{}{
		"vector": queryVector,
		"limit":  limit,
		"with": map[string]interface{}{
			"payload": true,  // 返回payload
			"vector":  false, // 不需要返回向量
		},
	}

	// 如果有过滤条件，添加到请求中
	if filter != nil && len(filter) > 0 {
		searchRequest["filter"] = filter
	}

	jsonData, err := json.Marshal(searchRequest)
	if err != nil {
		return nil, fmt.Errorf("序列化搜索请求失败: %w", err)
	}

	// 发送搜索请求
	url := fmt.Sprintf("%s/collections/%s/points/search", q.endpoint, q.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建搜索请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送搜索请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("搜索向量失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	// 解析响应
	var searchResp struct {
		Result []struct {
			ID      string                 `json:"id"`
			Score   float32                `json:"score"`
			Payload map[string]interface{} `json:"payload,omitempty"`
		} `json:"result"`
		Time float64 `json:"time"`
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取搜索响应失败: %w", err)
	}

	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("解析搜索响应失败: %w, 内容: %s", err, string(body))
	}

	// 转换为SearchResult结构
	results := make([]SearchResult, len(searchResp.Result))
	for i, r := range searchResp.Result {
		results[i] = SearchResult{
			ID:      r.ID,
			Score:   r.Score,
			Payload: r.Payload,
		}
	}

	return results, nil
}

// DeletePoints 删除向量点
func (q *Qdrant) DeletePoints(ctx context.Context, pointIDs []string) error {
	if len(pointIDs) == 0 {
		return nil
	}

	// 构建删除请求
	deleteRequest := map[string]interface{}{
		"points": pointIDs,
	}

	jsonData, err := json.Marshal(deleteRequest)
	if err != nil {
		return fmt.Errorf("序列化删除请求失败: %w", err)
	}

	// 发送请求
	url := fmt.Sprintf("%s/collections/%s/points/delete?wait=true", q.endpoint, q.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建删除请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送删除请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("删除点失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	log.Printf("成功删除 %d 个向量点", len(pointIDs))
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
