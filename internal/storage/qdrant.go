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

	"github.com/gofrs/uuid/v5" // 导入uuid包
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

// StoreResumeVectors 存储简历向量到Qdrant
func (q *Qdrant) StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float64) ([]string, error) {
	if len(chunks) != len(embeddings) {
		return nil, fmt.Errorf("块数量 (%d) 和嵌入数量 (%d) 不匹配", len(chunks), len(embeddings))
	}

	if len(chunks) == 0 {
		log.Printf("StoreResumeVectors: resumeID %s 没有块需要存储", resumeID)
		return []string{}, nil
	}

	// Qdrant 点结构
	type QdrantPoint struct {
		ID      string                 `json:"id"` // Qdrant 要求 ID 是 UUID 或 uint64
		Vector  []float64              `json:"vector"`
		Payload map[string]interface{} `json:"payload"`
	}

	points := make([]QdrantPoint, 0, len(chunks))
	storedPointIDs := make([]string, 0, len(chunks))

	// 从 resumeID (submission_uuid) 创建一个命名空间UUID
	// 这确保了对于给定的提交，其点ID是确定性的。
	namespaceUUID, err := uuid.FromString(resumeID)
	if err != nil {
		return nil, fmt.Errorf("无法从 resumeID '%s' 创建命名空间UUID: %w", resumeID, err)
	}

	for i, chunk := range chunks {
		if embeddings[i] == nil {
			log.Printf("StoreResumeVectors: resumeID %s, chunk %d 的嵌入向量为nil，跳过存储", resumeID, chunk.ChunkID)
			continue
		}

		// 为每个点生成确定性的UUIDv5作为其ID
		// 这确保了对同一份简历的同一分块的重复处理不会在Qdrant中创建重复的点，从而实现幂等性
		pointID := uuid.NewV5(namespaceUUID, fmt.Sprintf("chunk-%d", chunk.ChunkID)).String()

		// 构建载荷 (Payload)
		payload := map[string]interface{}{
			"resume_id":           resumeID,      // 关联回原始的简历提交UUID
			"original_chunk_id":   chunk.ChunkID, // 来自LLM分块器的原始chunk_id
			"chunk_type":          chunk.ChunkType,
			"content_preview":     truncateString(chunk.Content, 200), // 内容预览
			"importance_score":    chunk.ImportanceScore,
			"name":                chunk.UniqueIdentifiers.Name,
			"phone":               chunk.UniqueIdentifiers.Phone,
			"email":               chunk.UniqueIdentifiers.Email,
			"experience_years":    chunk.Metadata.ExperienceYears,
			"education_level":     chunk.Metadata.EducationLevel,
			"full_content_length": len(chunk.Content), // 存储完整内容的长度
			// 可以添加更多来自 chunk.UniqueIdentifiers 和 chunk.Metadata 的字段
		}
		// 如果需要存储完整内容，可以取消注释下一行，但要注意Qdrant对payload大小的潜在限制
		// payload["full_content"] = chunk.Content

		points = append(points, QdrantPoint{
			ID:      pointID,
			Vector:  embeddings[i],
			Payload: payload,
		})
		storedPointIDs = append(storedPointIDs, pointID)
	}

	if len(points) == 0 {
		log.Printf("StoreResumeVectors: resumeID %s 在过滤掉nil嵌入后没有有效的点需要存储", resumeID)
		return []string{}, nil
	}

	// 准备批量upsert请求
	upsertReqBody := map[string][]QdrantPoint{
		"points": points,
	}
	jsonData, err := json.Marshal(upsertReqBody)
	if err != nil {
		return nil, fmt.Errorf("序列化Qdrant点请求失败: %w", err)
	}

	// 发送请求
	url := fmt.Sprintf("%s/collections/%s/points?wait=true", q.endpoint, q.collectionName) // wait=true 确保操作完成
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("创建Qdrant点upsert请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("发送Qdrant点upsert请求失败: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body) // 读取响应体以供调试

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("qdrant点upsert失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	// 检查响应状态
	var qdrantResp struct {
		Result interface{} `json:"result"`
		Status string      `json:"status"`
		Time   float64     `json:"time"`
		Error  string      `json:"error,omitempty"` // 早期版本的 Qdrant 可能在 result 中包含 error
	}
	if err := json.Unmarshal(respBody, &qdrantResp); err != nil {
		log.Printf("StoreResumeVectors: 解析Qdrant upsert响应失败 for resumeID %s: %v. Body: %s", resumeID, err, string(respBody))
		// 即使解析失败，如果状态码是200，也可能操作已成功，但最好记录下来
	}

	if qdrantResp.Status != "ok" && qdrantResp.Status != "acknowledged" && qdrantResp.Status != "completed" {
		// "acknowledged" and "completed" are also valid success statuses for async operations or different versions.
		// For wait=true, "ok" or "completed" is more common.
		errMsg := fmt.Sprintf("Qdrant upsert 响应状态不是 'ok', 'acknowledged', or 'completed', 而是 '%s'", qdrantResp.Status)
		if qdrantResp.Error != "" {
			errMsg = fmt.Sprintf("%s. Qdrant Error: %s", errMsg, qdrantResp.Error)
		} else if resultError, ok := qdrantResp.Result.(map[string]interface{}); ok && resultError["error"] != nil {
			// 检查 result 字段中是否包含 error
			errMsg = fmt.Sprintf("%s. Qdrant Result Error: %v", errMsg, resultError["error"])
		}
		log.Printf("StoreResumeVectors: %s for resumeID %s. Full Response: %s", errMsg, resumeID, string(respBody))
		return nil, fmt.Errorf(errMsg)
	}

	log.Printf("成功存储/更新 %d 个向量点到Qdrant for resumeID %s. Point IDs: %v", len(points), resumeID, storedPointIDs)
	return storedPointIDs, nil
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
