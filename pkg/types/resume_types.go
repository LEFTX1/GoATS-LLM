package types

// ResumeChunk 表示简历的一个分块
type ResumeChunk struct {
	ChunkID           int           `json:"chunk_id"`
	ChunkType         string        `json:"chunk_type"`
	Content           string        `json:"content"`
	ImportanceScore   int           `json:"importance_score"`
	UniqueIdentifiers Identity      `json:"unique_identifiers"`
	Metadata          ChunkMetadata `json:"metadata"`
}

// Identity 表示简历中的唯一标识信息
type Identity struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

// ChunkMetadata 表示每个分块的元数据
type ChunkMetadata struct {
	Skills          []string `json:"skills,omitempty"`
	ExperienceYears int      `json:"experience_years,omitempty"`
	EducationLevel  string   `json:"education_level,omitempty"`
}

// ResumeData 表示整个简历的结构化数据
type ResumeData struct {
	ResumeID      string        `json:"resume_id"`
	CandidateInfo Identity      `json:"candidate_info"`
	Chunks        []ResumeChunk `json:"chunks"`
	Summary       struct {
		Skills          []string `json:"skills"`
		TotalExperience string   `json:"total_experience"`
		Education       string   `json:"education"`
	} `json:"summary"`
}
