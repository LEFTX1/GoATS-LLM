// Package storage 实现了与存储系统(MySQL和Qdrant)的交互
package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/types"
)

// QdrantClient 是Qdrant向量数据库的HTTP客户端
type QdrantClient struct {
	endpoint   string
	httpClient *http.Client
}

// NewQdrantClient 创建一个新的Qdrant客户端
func NewQdrantClient(cfg *config.Config) (*QdrantClient, error) {
	endpoint := cfg.Qdrant.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:6333" // 默认端点
	}

	return &QdrantClient{
		endpoint:   endpoint,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// EnsureCollection 确保Qdrant集合存在
func (q *QdrantClient) EnsureCollection(ctx context.Context, collectionName string, dimension int) error {
	// 先检查集合是否已存在
	url := fmt.Sprintf("%s/collections/%s", q.endpoint, collectionName)
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
		// 准备创建集合的请求
		createReqBody := map[string]interface{}{
			"vectors": map[string]interface{}{
				"size":     dimension,
				"distance": "Cosine", // 使用余弦相似度
			},
		}

		jsonData, err := json.Marshal(createReqBody)
		if err != nil {
			return fmt.Errorf("序列化创建集合请求失败: %w", err)
		}

		// 发送创建集合请求
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

		return nil
	} else if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("检查集合失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	return nil // 集合已存在
}

// UpsertVectors 更新或插入向量到Qdrant
func (q *QdrantClient) UpsertVectors(ctx context.Context, collectionName string, resumeID string, chunks []types.ResumeChunk, embeddings [][]float32) error {
	if len(chunks) != len(embeddings) {
		return fmt.Errorf("chunks和embeddings数量不匹配: %d vs %d", len(chunks), len(embeddings))
	}

	if len(chunks) == 0 {
		return nil // 没有数据要插入
	}

	// 构建Qdrant的点数据
	points := make([]map[string]interface{}, len(chunks))
	for i, chunk := range chunks {
		// 为每个chunk生成一个唯一的ID
		pointID := fmt.Sprintf("%s_chunk_%d", resumeID, chunk.ChunkID)

		// 构建payload
		payload := map[string]interface{}{
			"resume_id":        resumeID,
			"chunk_id":         chunk.ChunkID,
			"chunk_type":       chunk.ChunkType,
			"content_preview":  clientTruncateString(chunk.Content, 200),
			"importance_score": chunk.ImportanceScore,
		}

		// 添加元数据字段（如果有）
		if len(chunk.Metadata.Skills) > 0 {
			payload["skills"] = chunk.Metadata.Skills
		}
		if chunk.Metadata.ExperienceYears > 0 {
			payload["experience_years"] = chunk.Metadata.ExperienceYears
		}
		if chunk.Metadata.EducationLevel != "" {
			payload["education_level"] = chunk.Metadata.EducationLevel
		}

		// 创建点
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
		return fmt.Errorf("序列化upsert请求失败: %w", err)
	}

	// 发送请求
	url := fmt.Sprintf("%s/collections/%s/points?wait=true", q.endpoint, collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("创建upsert请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := q.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("发送upsert请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 检查响应
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("更新向量失败，状态码: %d, 响应: %s", resp.StatusCode, string(body))
	}

	return nil
}

// SearchResult 表示一个搜索结果项
type SearchResult struct {
	ID      string                 // 向量ID
	Score   float32                // 相似度分数
	Payload map[string]interface{} // 载荷数据
}

// SearchVectors 在Qdrant中搜索相似向量
func (q *QdrantClient) SearchVectors(ctx context.Context, collectionName string, vector []float32, limit int, filter map[string]interface{}) ([]SearchResult, error) {
	// 构建搜索请求
	searchRequest := map[string]interface{}{
		"vector": vector,
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
	url := fmt.Sprintf("%s/collections/%s/points/search", q.endpoint, collectionName)
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

// DeletePoints 从Qdrant中删除指定的点
func (q *QdrantClient) DeletePoints(ctx context.Context, collectionName string, pointIDs []string) error {
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
	url := fmt.Sprintf("%s/collections/%s/points/delete?wait=true", q.endpoint, collectionName)
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

	return nil
}

// 辅助函数 - 截断字符串
func clientTruncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen > 3 {
		return s[:maxLen-3] + "..."
	}
	return s[:maxLen]
}
