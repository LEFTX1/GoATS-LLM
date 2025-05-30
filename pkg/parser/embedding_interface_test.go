package parser_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ai-agent-go/pkg/parser"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// MockTextEmbedder is a mock type for the TextEmbedder interface
type MockTextEmbedder struct {
	mock.Mock
}

// EmbedStrings provides a mock function for TextEmbedder.EmbedStrings
func (m *MockTextEmbedder) EmbedStrings(ctx context.Context, texts []string) ([][]float64, error) {
	args := m.Called(ctx, texts)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([][]float64), args.Error(1)
}

func TestStandardResumeEmbedder_EmbedResumeChunks_Success(t *testing.T) {
	mockEmbedder := new(MockTextEmbedder)
	standardEmbedder := parser.NewStandardResumeEmbedder(mockEmbedder,
		parser.WithIncludeContent(true),
		parser.WithIncludeChunkType(true),
		parser.WithIncludeBasicInfo(true),
	)

	sections := []*parser.ResumeSection{
		{Type: "education", Title: "Education History", Content: "Master in AI"},
		{Type: "experience", Title: "Work Experience", Content: "Software Engineer at XYZ"},
		{Type: "skills", Title: "Technical Skills", Content: "Go, Python, Docker"},
		{Type: "project", Title: "Project X", Content: ""}, // Empty content section
	}
	basicInfo := map[string]string{
		"name":  "Test User",
		"email": "test@example.com",
	}

	expectedTexts := []string{"Master in AI", "Software Engineer at XYZ", "Go, Python, Docker"}
	mockedVectors := [][]float64{
		{0.1, 0.2, 0.3},
		{0.4, 0.5, 0.6},
		{0.7, 0.8, 0.9},
	}

	mockEmbedder.On("EmbedStrings", mock.Anything, expectedTexts).Return(mockedVectors, nil)

	ctx := context.Background()
	chunkVectors, err := standardEmbedder.EmbedResumeChunks(ctx, sections, basicInfo)

	require.NoError(t, err)
	require.NotNil(t, chunkVectors)
	require.Len(t, chunkVectors, 3, "Should only embed sections with content")

	// Verify a few things for the first chunk vector
	assert.Equal(t, sections[0], chunkVectors[0].Section)
	assert.Equal(t, mockedVectors[0], chunkVectors[0].Vector)
	assert.Equal(t, "education", chunkVectors[0].Metadata["section_type"])
	assert.Equal(t, "Education History", chunkVectors[0].Metadata["section_title"])
	assert.Equal(t, "Master in AI", chunkVectors[0].Metadata["content"])
	assert.Equal(t, "Test User", chunkVectors[0].Metadata["basic_name"])
	assert.Equal(t, "test@example.com", chunkVectors[0].Metadata["basic_email"])

	// Verify for the second chunk vector
	assert.Equal(t, sections[1], chunkVectors[1].Section)
	assert.Equal(t, mockedVectors[1], chunkVectors[1].Vector)
	assert.Equal(t, "experience", chunkVectors[1].Metadata["section_type"])

	mockEmbedder.AssertExpectations(t)
}

func TestStandardResumeEmbedder_EmbedResumeChunks_EmbedderError(t *testing.T) {
	mockEmbedder := new(MockTextEmbedder)
	standardEmbedder := parser.NewStandardResumeEmbedder(mockEmbedder)

	sections := []*parser.ResumeSection{
		{Content: "Some text"},
	}

	mockedError := errors.New("embedding failed")
	mockEmbedder.On("EmbedStrings", mock.Anything, []string{"Some text"}).Return(nil, mockedError)

	ctx := context.Background()
	_, err := standardEmbedder.EmbedResumeChunks(ctx, sections, nil)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "embedding failed")
	mockEmbedder.AssertExpectations(t)
}

func TestStandardResumeEmbedder_EmbedResumeChunks_NoContentToEmbed(t *testing.T) {
	mockEmbedder := new(MockTextEmbedder) // Mock should not be called
	standardEmbedder := parser.NewStandardResumeEmbedder(mockEmbedder)

	sections := []*parser.ResumeSection{
		{Type: "empty1", Content: ""},
		{Type: "empty2", Content: ""},
	}

	ctx := context.Background()
	chunkVectors, err := standardEmbedder.EmbedResumeChunks(ctx, sections, nil)

	require.Error(t, err, "Expected an error when no content is available to embed")
	assert.Nil(t, chunkVectors, "Chunk vectors should be nil when there is an error")
	assert.Contains(t, err.Error(), "没有有效的简历内容可以嵌入")
	mockEmbedder.AssertNotCalled(t, "EmbedStrings") // Ensure the embedder wasn't called
}

func TestStandardResumeEmbedder_EmbedResumeChunks_VectorMismatch(t *testing.T) {
	mockEmbedder := new(MockTextEmbedder)
	standardEmbedder := parser.NewStandardResumeEmbedder(mockEmbedder)

	sections := []*parser.ResumeSection{
		{Content: "Text1"},
		{Content: "Text2"},
	}

	// Mock returns only one vector for two texts
	mockedVectors := [][]float64{
		{0.1, 0.2},
	}
	mockEmbedder.On("EmbedStrings", mock.Anything, []string{"Text1", "Text2"}).Return(mockedVectors, nil)

	ctx := context.Background()
	_, err := standardEmbedder.EmbedResumeChunks(ctx, sections, nil)

	require.Error(t, err)
	expectedErrorString := fmt.Sprintf("嵌入向量数量(%d)与文本数量(%d)不匹配", len(mockedVectors), len(sections))
	assert.EqualError(t, err, expectedErrorString, "Error message should exactly match")
	mockEmbedder.AssertExpectations(t)
}

func TestStandardResumeEmbedder_EmbedResumeChunks_MetadataOptions(t *testing.T) {
	mockEmbedder := new(MockTextEmbedder)
	// Configure embedder to NOT include certain metadata
	standardEmbedder := parser.NewStandardResumeEmbedder(mockEmbedder,
		parser.WithIncludeContent(false),                      // Don't include content
		parser.WithIncludeChunkType(false),                    // Don't include chunk type
		parser.WithIncludeBasicInfo(false),                    // Don't include basic info
		parser.WithAdditionalFields([]string{"custom_field"}), // Include this custom field if present
	)

	sections := []*parser.ResumeSection{
		{Type: "test_type", Title: "Test Title", Content: "Some important content"},
	}
	basicInfo := map[string]string{
		"name":          "John Doe",
		"custom_field":  "custom_value",
		"ignored_field": "ignored_value",
	}

	mockedVectors := [][]float64{
		{1.0, 2.0, 3.0},
	}
	mockEmbedder.On("EmbedStrings", mock.Anything, []string{"Some important content"}).Return(mockedVectors, nil)

	ctx := context.Background()
	chunkVectors, err := standardEmbedder.EmbedResumeChunks(ctx, sections, basicInfo)

	require.NoError(t, err)
	require.Len(t, chunkVectors, 1)

	metadata := chunkVectors[0].Metadata
	assert.Nil(t, metadata["content"], "Content should not be in metadata")
	assert.Nil(t, metadata["section_type"], "Section type should not be in metadata")
	assert.Nil(t, metadata["section_title"], "Section title should not be in metadata")
	assert.Nil(t, metadata["basic_name"], "Basic info (name) should not be in metadata")
	assert.Equal(t, "custom_value", metadata["custom_field"], "Custom_field should be in metadata")
	assert.Nil(t, metadata["basic_ignored_field"], "Ignored basic info field should not be in metadata")

	mockEmbedder.AssertExpectations(t)
}
