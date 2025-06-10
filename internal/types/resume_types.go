package types

// SectionType 表示简历章节类型
type SectionType string

const (
	// SectionBasicInfo 基本信息章节
	SectionBasicInfo SectionType = "BASIC_INFO"
	// SectionEducation 教育经历章节
	SectionEducation SectionType = "EDUCATION"
	// SectionWorkExperience 工作经历章节
	SectionWorkExperience SectionType = "WORK_EXPERIENCE"
	// SectionSkills 技能章节
	SectionSkills SectionType = "SKILLS"
	// SectionProjects 项目经历章节
	SectionProjects SectionType = "PROJECTS"
	// SectionInternships 实习经历章节
	SectionInternships SectionType = "INTERNSHIPS"
	// SectionAwards 获奖经历章节
	SectionAwards SectionType = "AWARDS"
	// SectionResearch 研究经历章节
	SectionResearch SectionType = "RESEARCH"
	// SectionPortfolio 作品集章节
	SectionPortfolio SectionType = "PORTFOLIO"
	// SectionOtherExp 其他经历章节
	SectionOtherExp SectionType = "OTHER_EXPERIENCE"
	// SectionPersonalIntro 个人介绍章节
	SectionPersonalIntro SectionType = "PERSONAL_INTRO"
	// SectionUnknown 未分类内容章节
	SectionUnknown SectionType = "UNKNOWN"
	// SectionFullText 完整文本（未分块）
	SectionFullText SectionType = "FULL_TEXT"
)

// ResumeSection 简历章节结构
type ResumeSection struct {
	Type             SectionType // 章节类型
	Title            string      // 实际的章节标题
	Content          string      // 章节内容
	ChunkID          int         // 分块ID
	ResumeIdentifier string      // 简历标识符
}

// ResumeChunkVector 表示简历分块的向量表示
type ResumeChunkVector struct {
	// 源内容
	Section *ResumeSection

	// 向量表示
	Vector []float64

	// 元数据
	Metadata map[string]interface{}
}

// JobMatchEvaluation 岗位匹配评估结果
type JobMatchEvaluation struct {
	// 匹配分数 (0-100)
	MatchScore int `json:"match_score"`

	// 匹配亮点
	MatchHighlights []string `json:"match_highlights"`

	// 潜在不足
	PotentialGaps []string `json:"potential_gaps"`

	// 针对岗位的简历摘要
	ResumeSummaryForJD string `json:"resume_summary_for_jd"`

	// 评估时间
	EvaluatedAt int64 `json:"evaluated_at"`

	// 评估ID
	EvaluationID string `json:"evaluation_id,omitempty"`
}

// ResumeChunk 表示简历的一个分块
type ResumeChunk struct {
	ChunkID           int           `json:"chunk_id"`
	ChunkType         string        `json:"chunk_type"`
	Content           string        `json:"content"`
	ImportanceScore   float32       `json:"importance_score"`
	UniqueIdentifiers Identity      `json:"unique_identifiers"`
	Metadata          ChunkMetadata `json:"metadata"`
	ChunkTitle        string        `json:"chunk_title,omitempty"` // 可选标题，适用于有标题的分块
}

// Identity 表示简历中的唯一标识信息
type Identity struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
}

// ChunkMetadata 表示每个分块的元数据
type ChunkMetadata struct {
	ExperienceYears int    `json:"experience_years,omitempty"`
	EducationLevel  string `json:"education_level,omitempty"`
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

// RankedSubmission holds the final aggregated score for a submission, used in search results.
type RankedSubmission struct {
	SubmissionUUID string
	Score          float32
}

// PaginatedResumeResponse 分页简历响应
type PaginatedResumeResponse struct {
	JobID      string             `json:"job_id"`
	Cursor     int64              `json:"cursor"`
	NextCursor int64              `json:"next_cursor"`
	Size       int64              `json:"size"`
	TotalCount int64              `json:"total_count"`
	Resumes    []ResumeWithChunks `json:"resumes"`
}

// ResumeWithChunks 包含所有块的简历
type ResumeWithChunks struct {
	SubmissionUUID string            `json:"submission_uuid"`
	BasicInfo      map[string]string `json:"basic_info"`
	Chunks         []ResumeChunkData `json:"chunks"`
}

// ResumeChunkData 简历块数据
type ResumeChunkData struct {
	ChunkID     int    `json:"chunk_id"`
	ChunkType   string `json:"chunk_type"`
	ChunkTitle  string `json:"chunk_title"`
	ContentText string `json:"content_text"`
}
