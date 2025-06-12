package models

import (
	"ai-agent-go/internal/types"
	"encoding/json"
	"time"

	"gorm.io/datatypes"
)

// ResumeMeta 存储简历的元数据信息
type ResumeMeta struct {
	// 基本标识
	ResumeID       string `gorm:"primaryKey" json:"resume_id"`
	SubmissionUUID string `gorm:"index" json:"submission_uuid"`

	// 学校相关信息
	Is211            bool   `json:"is_211"`
	Is985            bool   `json:"is_985"`
	IsDoubleTop      bool   `json:"is_double_top"`
	HighestEducation string `json:"highest_education"` // 本科/硕士/博士/专科

	// 经验相关
	YearsOfExperience float32 `json:"years_of_experience"`
	HasInternExp      bool    `json:"has_intern_exp"`
	HasWorkExp        bool    `json:"has_work_exp"`

	// 奖项相关
	HasAlgorithmAward              bool           `json:"has_algorithm_award"`
	AlgorithmAwardTitles           datatypes.JSON `json:"algorithm_award_titles"` // string[]
	HasProgrammingCompetitionAward bool           `json:"has_programming_competition_award"`
	ProgrammingCompetitionTitles   datatypes.JSON `json:"programming_competition_titles"` // string[]

	// 评分和标签
	ResumeScore int            `json:"resume_score"`
	Tags        datatypes.JSON `json:"tags"` // string[]

	// 额外元数据（JSON格式）
	MetaExtra datatypes.JSON `json:"meta_extra,omitempty"`

	// 时间戳
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ToResumeMetadata 将数据库模型转换为领域模型
func (r *ResumeMeta) ToResumeMetadata() types.ResumeMetadata {
	var algoTitles, progTitles, tags []string

	// 解析JSON字段
	if len(r.AlgorithmAwardTitles) > 0 {
		_ = json.Unmarshal(r.AlgorithmAwardTitles, &algoTitles)
	}
	if len(r.ProgrammingCompetitionTitles) > 0 {
		_ = json.Unmarshal(r.ProgrammingCompetitionTitles, &progTitles)
	}
	if len(r.Tags) > 0 {
		_ = json.Unmarshal(r.Tags, &tags)
	}

	return types.ResumeMetadata{
		Is211:                          r.Is211,
		Is985:                          r.Is985,
		IsDoubleTop:                    r.IsDoubleTop,
		HighestEducation:               r.HighestEducation,
		YearsOfExperience:              r.YearsOfExperience,
		HasInternExp:                   r.HasInternExp,
		HasWorkExp:                     r.HasWorkExp,
		HasAlgorithmAward:              r.HasAlgorithmAward,
		AlgorithmAwardTitles:           algoTitles,
		HasProgrammingCompetitionAward: r.HasProgrammingCompetitionAward,
		ProgrammingCompetitionTitles:   progTitles,
		ResumeScore:                    r.ResumeScore,
		Tags:                           tags,
	}
}

// FromResumeMetadata 从领域模型创建数据库模型
func (r *ResumeMeta) FromResumeMetadata(metadata types.ResumeMetadata) error {
	// 填充基本字段
	r.Is211 = metadata.Is211
	r.Is985 = metadata.Is985
	r.IsDoubleTop = metadata.IsDoubleTop
	r.HighestEducation = metadata.HighestEducation
	r.YearsOfExperience = metadata.YearsOfExperience
	r.HasInternExp = metadata.HasInternExp
	r.HasWorkExp = metadata.HasWorkExp
	r.HasAlgorithmAward = metadata.HasAlgorithmAward
	r.HasProgrammingCompetitionAward = metadata.HasProgrammingCompetitionAward
	r.ResumeScore = metadata.ResumeScore

	// 转换数组为JSON
	if len(metadata.AlgorithmAwardTitles) > 0 {
		jsonBytes, err := json.Marshal(metadata.AlgorithmAwardTitles)
		if err == nil {
			r.AlgorithmAwardTitles = datatypes.JSON(jsonBytes)
		}
	}

	if len(metadata.ProgrammingCompetitionTitles) > 0 {
		jsonBytes, err := json.Marshal(metadata.ProgrammingCompetitionTitles)
		if err == nil {
			r.ProgrammingCompetitionTitles = datatypes.JSON(jsonBytes)
		}
	}

	if len(metadata.Tags) > 0 {
		jsonBytes, err := json.Marshal(metadata.Tags)
		if err == nil {
			r.Tags = datatypes.JSON(jsonBytes)
		}
	}

	return nil
}
