package models

import (
	"encoding/json"
	"time"

	"gorm.io/datatypes"
)

// Candidate 候选人主表
type Candidate struct {
	CandidateID     string          `gorm:"type:char(36);primaryKey"`
	PrimaryName     string          `gorm:"type:varchar(255)"`
	PrimaryPhone    string          `gorm:"type:varchar(50);uniqueIndex:idx_candidates_primary_phone_unique"`
	PrimaryEmail    string          `gorm:"type:varchar(255);uniqueIndex:idx_candidates_primary_email_unique"`
	Gender          string          `gorm:"type:varchar(10)"`
	BirthDate       *datatypes.Date `gorm:"type:date"` // Changed to datatypes.Date for SQL DATE type
	CurrentLocation string          `gorm:"type:varchar(255)"`
	ProfileSummary  string          `gorm:"type:text"`
	CreatedAt       time.Time       `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt       time.Time       `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`
}

func (Candidate) TableName() string {
	return "candidates"
}

// Job 岗位信息表
type Job struct {
	JobID                      string         `gorm:"type:char(36);primaryKey"`
	JobTitle                   string         `gorm:"type:varchar(255);not null"`
	Department                 string         `gorm:"type:varchar(255)"`
	Location                   string         `gorm:"type:varchar(255)"`
	JobDescriptionText         string         `gorm:"type:text;not null"`
	StructuredRequirementsJSON datatypes.JSON `gorm:"type:json"`
	JDSkillsKeywordsJSON       datatypes.JSON `gorm:"type:json"`
	Status                     string         `gorm:"type:varchar(50);default:'ACTIVE';index:idx_jobs_status"`
	CreatedByUserID            string         `gorm:"type:char(36)"`
	CreatedAt                  time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt                  time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`
}

func (Job) TableName() string {
	return "jobs"
}

// JobVector 存储岗位的向量表示
type JobVector struct {
	JobID                 string    `gorm:"type:char(36);primaryKey"`
	VectorRepresentation  []byte    `gorm:"type:mediumblob;not null"` // 存储序列化后的向量
	EmbeddingModelVersion string    `gorm:"type:varchar(100);not null"`
	CreatedAt             time.Time `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt             time.Time `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`
	Job                   Job       `gorm:"foreignKey:JobID;references:JobID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (JobVector) TableName() string {
	return "job_vectors"
}

// ResumeSubmission 简历提交/快照表
type ResumeSubmission struct {
	SubmissionUUID      string         `gorm:"type:char(36);primaryKey"`
	CandidateID         *string        `gorm:"type:char(36);index:idx_rs_candidate_id"` // Changed to *string (nullable for ON DELETE SET NULL)
	SubmissionTimestamp time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);index:idx_rs_submission_timestamp"`
	SourceChannel       string         `gorm:"type:varchar(100)"`
	TargetJobID         *string        `gorm:"type:char(36);index:idx_rs_target_job_id"` // Changed to *string (nullable for ON DELETE SET NULL)
	OriginalFilename    string         `gorm:"type:varchar(255)"`
	OriginalFilePathOSS string         `gorm:"type:varchar(1024)"`
	ParsedTextPathOSS   string         `gorm:"type:varchar(1024)"`
	RawTextMD5          string         `gorm:"type:char(32);index:idx_rs_raw_text_md5"`
	SimilarityHash      *string        `gorm:"type:char(16);uniqueIndex:idx_rs_similarity_hash"` // 新增字段，注意设为指针以允许NULL
	LLMParsedBasicInfo  datatypes.JSON `gorm:"type:json"`
	LLMResumeIdentifier string         `gorm:"type:varchar(255)"`
	ProcessingStatus    string         `gorm:"type:varchar(50);default:'PENDING_PARSING';index:idx_rs_processing_status"`
	ParserVersion       string         `gorm:"type:varchar(50)"`
	CreatedAt           time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt           time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`

	Candidate *Candidate `gorm:"foreignKey:CandidateID;references:CandidateID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL"`
	Job       *Job       `gorm:"foreignKey:TargetJobID;references:JobID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL"`
}

func (ResumeSubmission) TableName() string {
	return "resume_submissions"
}

// ResumeSubmissionChunk 简历分块文本表
type ResumeSubmissionChunk struct {
	ChunkDBID           uint64    `gorm:"primaryKey;autoIncrement"`
	SubmissionUUID      string    `gorm:"type:char(36);not null;index:idx_rsc_submission_uuid;uniqueIndex:idx_rsc_submission_chunk_id,priority:1"`
	ChunkIDInSubmission int       `gorm:"not null;uniqueIndex:idx_rsc_submission_chunk_id,priority:2"`
	ChunkType           string    `gorm:"type:varchar(50);not null;index:idx_rsc_chunk_type"`
	ChunkTitle          string    `gorm:"type:text"`
	ChunkContentText    string    `gorm:"type:text;not null"`
	CreatedAt           time.Time `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`

	ResumeSubmission *ResumeSubmission `gorm:"foreignKey:SubmissionUUID;references:SubmissionUUID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (ResumeSubmissionChunk) TableName() string {
	return "resume_submission_chunks"
}

// JobSubmissionMatch 岗位-简历投递匹配评估表
type JobSubmissionMatch struct {
	MatchID                uint64         `gorm:"primaryKey;autoIncrement"`
	SubmissionUUID         string         `gorm:"type:char(36);not null;index:idx_jsm_submission_uuid;uniqueIndex:idx_jsm_submission_job_unique,priority:1"`
	JobID                  string         `gorm:"type:char(36);not null;index:idx_jsm_job_id_overall_score,priority:1;uniqueIndex:idx_jsm_submission_job_unique,priority:2"`
	LLMMatchScore          *int           `gorm:"type:int"`
	LLMMatchHighlightsJSON datatypes.JSON `gorm:"type:json"`
	LLMPotentialGapsJSON   datatypes.JSON `gorm:"type:json"`
	LLMResumeSummaryForJD  string         `gorm:"type:text"`
	VectorMatchScore       *float64       `gorm:"type:float"`
	VectorScoreDetailsJSON datatypes.JSON `gorm:"type:json"`
	OverallCalculatedScore *float64       `gorm:"type:float;index:idx_jsm_job_id_overall_score,priority:2"`
	EvaluationStatus       string         `gorm:"type:varchar(50);default:'PENDING';index:idx_jsm_evaluation_status"`
	EvaluatedAt            *time.Time     `gorm:"type:datetime(6)"`
	HRFeedbackStatus       string         `gorm:"type:varchar(50);index:idx_jsm_hr_feedback_status"`
	HRFeedbackNotes        string         `gorm:"type:text"`
	CreatedAt              time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt              time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`

	ResumeSubmission *ResumeSubmission `gorm:"foreignKey:SubmissionUUID;references:SubmissionUUID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	Job              *Job              `gorm:"foreignKey:JobID;references:JobID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
}

func (JobSubmissionMatch) TableName() string {
	return "job_submission_matches"
}

// Interviewer 面试官信息表
type Interviewer struct {
	InterviewerID string    `gorm:"type:char(36);primaryKey"`
	UserID        *string   `gorm:"type:char(36);unique"` // Changed to *string (optional/nullable)
	Name          string    `gorm:"type:varchar(255);not null"`
	Email         string    `gorm:"type:varchar(255);unique;not null"`
	Department    string    `gorm:"type:varchar(255)"`
	Title         string    `gorm:"type:varchar(255)"`
	IsActive      bool      `gorm:"default:true"`
	CreatedAt     time.Time `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt     time.Time `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`
}

func (Interviewer) TableName() string {
	return "interviewers"
}

// InterviewEvaluation 面试评价表
type InterviewEvaluation struct {
	EvaluationID         uint64         `gorm:"primaryKey;autoIncrement"`
	CandidateID          string         `gorm:"type:char(36);not null;index:idx_ie_candidate_id"`
	JobID                string         `gorm:"type:char(36);not null;index:idx_ie_job_id"`
	SubmissionUUID       *string        `gorm:"type:char(36);index:idx_ie_submission_uuid"` // Changed to *string (optional/nullable for ON DELETE SET NULL)
	InterviewerID        string         `gorm:"type:char(36);not null;index:idx_ie_interviewer_id"`
	InterviewRound       string         `gorm:"type:varchar(100);not null"`
	InterviewDate        time.Time      `gorm:"type:datetime(6);not null;index:idx_ie_interview_date"`
	OverallRating        *int           `gorm:"type:int"`
	EvaluationSummary    string         `gorm:"type:text"`
	Strengths            string         `gorm:"type:text"`
	Weaknesses           string         `gorm:"type:text"`
	SpecificSkillRatings datatypes.JSON `gorm:"type:json"`
	HiringRecommendation string         `gorm:"type:varchar(50)"`
	Notes                string         `gorm:"type:text"`
	CreatedAt            time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt            time.Time      `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`

	Candidate        *Candidate        `gorm:"foreignKey:CandidateID;references:CandidateID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	Job              *Job              `gorm:"foreignKey:JobID;references:JobID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	ResumeSubmission *ResumeSubmission `gorm:"foreignKey:SubmissionUUID;references:SubmissionUUID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL"`
	Interviewer      *Interviewer      `gorm:"foreignKey:InterviewerID;references:InterviewerID;constraint:OnUpdate:CASCADE,OnDelete:RESTRICT"`
}

func (InterviewEvaluation) TableName() string {
	return "interview_evaluations"
}

// ReviewedResume HR审阅简历记录表
type ReviewedResume struct {
	ReviewID       uint64    `gorm:"primaryKey;autoIncrement"`
	JobID          string    `gorm:"type:char(36);not null;uniqueIndex:uq_reviewed_job_hr_text_md5,priority:1"`
	HRID           string    `gorm:"type:char(36);not null;uniqueIndex:uq_reviewed_job_hr_text_md5,priority:2"` // 假设HR用户ID也是char(36)
	TextMD5        string    `gorm:"type:char(32);not null;uniqueIndex:uq_reviewed_job_hr_text_md5,priority:3"`
	SubmissionUUID *string   `gorm:"type:char(36)"` // 可空
	Action         string    `gorm:"type:varchar(50);not null"`
	ReasonText     string    `gorm:"type:text"`
	IdempotencyKey *string   `gorm:"type:char(36);uniqueIndex:uq_reviewed_idempotency_key"` // 可空
	Version        int       `gorm:"default:1"`
	CreatedAt      time.Time `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6)"`
	UpdatedAt      time.Time `gorm:"type:datetime(6);default:CURRENT_TIMESTAMP(6);autoUpdateTime"`

	Job              *Job              `gorm:"foreignKey:JobID;references:JobID;constraint:OnUpdate:CASCADE,OnDelete:CASCADE"`
	ResumeSubmission *ResumeSubmission `gorm:"foreignKey:SubmissionUUID;references:SubmissionUUID;constraint:OnUpdate:CASCADE,OnDelete:SET NULL"`
	// HRUser *HRUser `gorm:"foreignKey:HRID..."` // 如果有HR用户表的话
}

func (ReviewedResume) TableName() string {
	return "reviewed_resumes"
}

// StringToJSON StringToJSON Helper function to convert string to datatypes.JSON
func StringToJSON(s string) datatypes.JSON {
	return datatypes.JSON(s)
}

// MapToJSON MapToJSON Helper function to convert map[string]interface{} to datatypes.JSON
func MapToJSON(m map[string]interface{}) (datatypes.JSON, error) {
	bytes, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// StringMapToJSON Helper function to convert map[string]string to datatypes.JSON
func StringMapToJSON(m map[string]string) (datatypes.JSON, error) {
	bytes, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}
