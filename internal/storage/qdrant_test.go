package storage_test

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/types"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQdrant_NewQdrant 测试Qdrant客户端初始化
func TestQdrant_NewQdrant(t *testing.T) {
	// 创建一个模拟的HTTP服务器来模拟Qdrant API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 检查请求路径
		if r.URL.Path == "/collections/test_collection" && r.Method == "GET" {
			// 返回集合存在的响应
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"result": {
					"config": {
						"params": {
							"vectors": {
								"size": 1024,
								"distance": "Cosine"
							}
						}
					}
				}
			}`))
			return
		}
		// 默认返回404
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// 创建Qdrant配置
	cfg := &config.QdrantConfig{
		Endpoint:   server.URL,
		Collection: "test_collection",
		Dimension:  1024,
	}

	// 使用选项模式创建客户端
	client, err := storage.NewQdrant(cfg,
		storage.WithDistanceMetric("Cosine"),
		storage.WithHttpTimeout(5*time.Second))

	require.NoError(t, err, "应该成功创建Qdrant客户端")
	require.NotNil(t, client, "客户端不应为nil")
}

// TestQdrant_StoreResumeVectors_WithFloat64 测试存储float64类型向量
func TestQdrant_StoreResumeVectors_WithFloat64(t *testing.T) {
	// 创建一个模拟的HTTP服务器来模拟Qdrant API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test_collection" && r.Method == "GET" {
			// 返回集合存在的响应
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result": {"config": {"params": {"vectors": {"size": 1024, "distance": "Cosine"}}}}}`))
			return
		}

		if r.URL.Path == "/collections/test_collection/points" && r.Method == "PUT" {
			// 返回存储成功响应
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result": {"operation_id": 123, "status": "completed"}}`))
			return
		}

		// 默认返回404
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// 创建Qdrant配置
	cfg := &config.QdrantConfig{
		Endpoint:   server.URL,
		Collection: "test_collection",
		Dimension:  1024,
	}

	// 创建客户端
	client, err := storage.NewQdrant(cfg)
	require.NoError(t, err, "应该成功创建Qdrant客户端")

	// 创建测试数据
	resumeID := "test-resume-123"
	chunks := []types.ResumeChunk{
		{
			ChunkID:   1,
			ChunkType: "summary",
			Content:   "这是一份测试简历",
		},
	}

	// 创建float64向量进行测试
	embeddings := [][]float64{
		make([]float64, 1024),
	}
	// 填充一些测试数据
	for i := 0; i < 1024; i++ {
		embeddings[0][i] = float64(i) / 1024.0
	}

	// 存储向量
	ctx := context.Background()
	pointIDs, err := client.StoreResumeVectors(ctx, resumeID, chunks, embeddings)

	require.NoError(t, err, "向量存储应成功")
	require.Len(t, pointIDs, 1, "应返回一个点ID")

	// 验证返回的是有效的UUID格式
	_, uuidErr := uuid.FromString(pointIDs[0])
	require.NoError(t, uuidErr, "返回的pointID应为有效的UUID格式")

	// 验证多次调用生成的ID一致（确定性ID）
	pointIDs2, err := client.StoreResumeVectors(ctx, resumeID, chunks, embeddings)
	require.NoError(t, err, "第二次存储向量应成功")
	require.Len(t, pointIDs2, 1, "第二次应返回一个点ID")
	assert.Equal(t, pointIDs[0], pointIDs2[0], "对相同数据的多次操作应生成相同的pointID")
}

// TestQdrant_SearchSimilarResumes_WithFloat64 测试使用float64类型向量进行搜索
func TestQdrant_SearchSimilarResumes_WithFloat64(t *testing.T) {
	// 创建一个模拟的HTTP服务器来模拟Qdrant API
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/collections/test_collection" && r.Method == "GET" {
			// 返回集合存在的响应
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"result": {"config": {"params": {"vectors": {"size": 1024, "distance": "Cosine"}}}}}`))
			return
		}

		if r.URL.Path == "/collections/test_collection/points/search" && r.Method == "POST" {
			// 返回搜索结果响应
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{
				"result": [
					{
						"id": "e5f3bbc0-fc9d-5f22-9a84-a501a612d755",
						"score": 0.95,
						"payload": {
							"resume_id": "test-resume-123",
							"original_chunk_id": 1,
							"chunk_type": "summary",
							"content_preview": "这是一份测试简历",
							"skills": ["Go", "Python"]
						}
					}
				],
				"time": 0.001
			}`))
			return
		}

		// 默认返回404
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	// 创建Qdrant配置
	cfg := &config.QdrantConfig{
		Endpoint:   server.URL,
		Collection: "test_collection",
		Dimension:  1024,
	}

	// 创建客户端
	client, err := storage.NewQdrant(cfg)
	require.NoError(t, err, "应该成功创建Qdrant客户端")

	// 创建float64查询向量
	queryVector := make([]float64, 1024)
	for i := 0; i < 1024; i++ {
		queryVector[i] = float64(i) / 1024.0
	}

	// 进行搜索
	ctx := context.Background()
	results, err := client.SearchSimilarResumes(ctx, queryVector, 10, nil)

	require.NoError(t, err, "向量搜索应成功")
	require.Len(t, results, 1, "应返回一个结果")

	// 验证返回的是有效的UUID格式
	_, uuidErr := uuid.FromString(results[0].ID)
	require.NoError(t, uuidErr, "返回的结果ID应为有效的UUID格式")

	assert.InDelta(t, 0.95, float64(results[0].Score), 0.01, "结果分数应符合预期")

	// 验证payload包含正确的数据
	assert.Equal(t, "test-resume-123", results[0].Payload["resume_id"], "resume_id应匹配")
	assert.Equal(t, float64(1), results[0].Payload["original_chunk_id"], "original_chunk_id应匹配")
	assert.Equal(t, "summary", results[0].Payload["chunk_type"], "chunk_type应匹配")
}
