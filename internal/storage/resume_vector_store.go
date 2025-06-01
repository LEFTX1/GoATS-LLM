package storage

import (
	"ai-agent-go/internal/types"
	"context"
)

// ResumeVectorStore 提供简历向量存储功能
type ResumeVectorStore struct {
	VectorDB VectorDatabase
}

// StoreResumeVectors 存储简历向量
func (s *ResumeVectorStore) StoreResumeVectors(ctx context.Context, resumeID string, chunks []types.ResumeChunk, embeddings [][]float64) ([]string, error) {
	if s.VectorDB == nil {
		return nil, nil
	}
	return s.VectorDB.StoreResumeVectors(ctx, resumeID, chunks, embeddings)
}

// SearchSimilarResumes 搜索相似简历
func (s *ResumeVectorStore) SearchSimilarResumes(ctx context.Context, queryVector []float64, limit int, filter map[string]interface{}) ([]SearchResult, error) {
	if s.VectorDB == nil {
		return nil, nil
	}
	return s.VectorDB.SearchSimilarResumes(ctx, queryVector, limit, filter)
}
