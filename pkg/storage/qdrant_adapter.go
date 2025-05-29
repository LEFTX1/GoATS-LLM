package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/types"
)

const (
	defaultQdrantURL      = "http://localhost:6333"
	defaultCollectionName = "resume_chunks_collection"
	defaultVectorSize     = 768
	defaultDistanceMetric = "Cosine"
)

// ResumeVectorStore 简历向量存储接口
type ResumeVectorStore struct {
	adapter        *QdrantAdapter
	collectionName string
	dimension      int
}

// NewResumeVectorStore 创建新的简历向量存储
func NewResumeVectorStore(cfg *config.Config) (*ResumeVectorStore, error) {
	adapter, err := NewQdrantAdapter(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create Qdrant adapter: %w", err)
	}

	return &ResumeVectorStore{
		adapter:        adapter,
		collectionName: cfg.Qdrant.Collection,
		dimension:      cfg.Qdrant.Dimension,
	}, nil
}

// StoreResumeVectors 存储简历向量
func (s *ResumeVectorStore) StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float32) ([]string, error) {
	return s.adapter.AddResumeChunks(ctx, resumeID, chunks, embeddings)
}

// SearchSimilarResumes 搜索相似简历
func (s *ResumeVectorStore) SearchSimilarResumes(ctx context.Context, queryVector []float32, limit int, filter map[string]interface{}) ([]SearchResult, error) {
	return s.adapter.SearchSimilarChunks(ctx, queryVector, limit, filter)
}

// QdrantAdapter 负责与Qdrant向量数据库的交互
type QdrantAdapter struct {
	httpClient     *http.Client
	qdrantURL      string
	collectionName string
	vectorSize     int
	distance       string
}

// NewQdrantAdapter 创建一个新的QdrantAdapter实例
func NewQdrantAdapter(cfg *config.Config) (*QdrantAdapter, error) {
	url := cfg.Qdrant.Endpoint
	if strings.TrimSpace(url) == "" {
		url = defaultQdrantURL
	}
	collName := cfg.Qdrant.Collection
	if strings.TrimSpace(collName) == "" {
		collName = defaultCollectionName
	}
	vSize := cfg.Qdrant.Dimension
	if vSize <= 0 {
		vSize = defaultVectorSize
	}
	dist := defaultDistanceMetric

	adapter := &QdrantAdapter{
		httpClient:     &http.Client{},
		qdrantURL:      url,
		collectionName: collName,
		vectorSize:     vSize,
		distance:       dist,
	}

	if err := adapter.ensureCollectionExists(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to ensure Qdrant collection exists: %w", err)
	}
	log.Printf("Successfully connected to Qdrant and ensured collection '%s' exists.", collName)
	return adapter, nil
}

func (a *QdrantAdapter) ensureCollectionExists(ctx context.Context) error {
	collectionURL := fmt.Sprintf("%s/collections/%s", a.qdrantURL, a.collectionName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, collectionURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create request to get collection info: %w", err)
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get collection info from Qdrant: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		// 解析集合信息
		var collInfo struct {
			Result struct {
				Status string `json:"status"`
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

		if err := json.Unmarshal(bodyBytes, &collInfo); err == nil {
			if collInfo.Result.Config.Params.Vectors.Size == a.vectorSize &&
				collInfo.Result.Config.Params.Vectors.Distance == a.distance {
				log.Printf("Qdrant collection '%s' already exists with correct configuration.", a.collectionName)
				return nil
			}
			return fmt.Errorf("collection '%s' exists but with different configuration (size: %d vs %d, distance: %s vs %s)",
				a.collectionName, collInfo.Result.Config.Params.Vectors.Size, a.vectorSize,
				collInfo.Result.Config.Params.Vectors.Distance, a.distance)
		}
		log.Printf("Qdrant collection '%s' exists, but failed to parse its info. Assuming compatible: %s", a.collectionName, string(bodyBytes))
		return nil
	} else if resp.StatusCode == http.StatusNotFound {
		log.Printf("Qdrant collection '%s' not found. Creating it...", a.collectionName)
		return a.createCollection(ctx)
	} else {
		return fmt.Errorf("failed to check Qdrant collection '%s': status %d, body: %s", a.collectionName, resp.StatusCode, string(bodyBytes))
	}
}

func (a *QdrantAdapter) createCollection(ctx context.Context) error {
	createURL := fmt.Sprintf("%s/collections/%s", a.qdrantURL, a.collectionName)

	// 创建请求体
	payload := map[string]interface{}{
		"vectors": map[string]interface{}{
			"size":     a.vectorSize,
			"distance": a.distance,
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal create collection request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, createURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request for creating collection: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send create collection request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to create Qdrant collection '%s': status %d, body: %s", a.collectionName, resp.StatusCode, string(bodyBytes))
	}
	log.Printf("Successfully created Qdrant collection '%s'", a.collectionName)
	return nil
}

func (a *QdrantAdapter) AddResumeChunks(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float32) ([]string, error) {
	if len(chunks) != len(embeddings) {
		return nil, fmt.Errorf("number of chunks (%d) and embeddings (%d) must match", len(chunks), len(embeddings))
	}
	if len(chunks) == 0 {
		return []string{}, nil
	}

	points := make([]map[string]interface{}, len(chunks))
	pointIDs := make([]string, len(chunks))

	for i, chunk := range chunks {
		pointID := fmt.Sprintf("%s_chunk_%d", resumeID, chunk.ChunkID)
		pointIDs[i] = pointID

		payload := map[string]interface{}{
			"resume_id":           resumeID,
			"chunk_sequential_id": chunk.ChunkID,
			"chunk_type":          chunk.ChunkType,
			"importance_score":    chunk.ImportanceScore,
			"content_preview":     clientTruncateString(chunk.Content, 200),
		}
		if len(chunk.Metadata.Skills) > 0 {
			payload["skills"] = chunk.Metadata.Skills
		}
		if chunk.Metadata.ExperienceYears > 0 {
			payload["experience_years"] = chunk.Metadata.ExperienceYears
		}
		if chunk.Metadata.EducationLevel != "" {
			payload["education_level"] = chunk.Metadata.EducationLevel
		}

		points[i] = map[string]interface{}{
			"id":      pointID,
			"vector":  embeddings[i],
			"payload": payload,
		}
	}

	upsertURL := fmt.Sprintf("%s/collections/%s/points?wait=true", a.qdrantURL, a.collectionName)
	requestBody := map[string]interface{}{
		"points": points,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal upsert points request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, upsertURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request for upserting points: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send upsert points request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to upsert points to Qdrant collection '%s': status %d, body: %s", a.collectionName, resp.StatusCode, string(bodyBytes))
	}
	log.Printf("Successfully upserted %d points to Qdrant collection '%s' for resume %s.", len(chunks), a.collectionName, resumeID)
	return pointIDs, nil
}

func (a *QdrantAdapter) SearchSimilarChunks(ctx context.Context, queryVector []float32, topK int, filter map[string]interface{}) ([]SearchResult, error) {
	searchURL := fmt.Sprintf("%s/collections/%s/points/search", a.qdrantURL, a.collectionName)

	searchRequest := map[string]interface{}{
		"vector": queryVector,
		"limit":  topK,
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
		return nil, fmt.Errorf("failed to marshal search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request for searching points: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send search request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to search points in Qdrant collection '%s': status %d, body: %s", a.collectionName, resp.StatusCode, string(bodyBytes))
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

	if err := json.Unmarshal(bodyBytes, &searchResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal search response from Qdrant: %w. Body: %s", err, string(bodyBytes))
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

func (a *QdrantAdapter) DeletePoints(ctx context.Context, pointIDs []string) error {
	if len(pointIDs) == 0 {
		return nil
	}

	deleteURL := fmt.Sprintf("%s/collections/%s/points/delete?wait=true", a.qdrantURL, a.collectionName)
	payload := map[string]interface{}{
		"points": pointIDs,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal delete points request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deleteURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request for deleting points: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send delete points request to Qdrant: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to delete points from Qdrant collection '%s': status %d, body: %s", a.collectionName, resp.StatusCode, string(bodyBytes))
	}
	log.Printf("Successfully deleted %d points from Qdrant collection '%s'.", len(pointIDs), a.collectionName)
	return nil
}

func convertToUint64Slice(s []string) ([]uint64, error) {
	result := make([]uint64, len(s))
	for i, strID := range s {
		val, err := strconv.ParseUint(strID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("failed to convert point ID '%s' to uint64: %w", strID, err)
		}
		result[i] = val
	}
	return result, nil
}
