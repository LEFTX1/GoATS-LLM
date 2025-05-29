package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"ai-agent-go/pkg/types" // 修改为导入 types 包

	_ "github.com/go-sql-driver/mysql"
)

// MySQLAdapter 负责与MySQL数据库的交互
type MySQLAdapter struct {
	db *sql.DB
}

// NewMySQLAdapter 创建一个新的MySQLAdapter实例并初始化数据库连接和表结构
func NewMySQLAdapter(dataSourceName string) (*MySQLAdapter, error) {
	db, err := sql.Open("mysql", dataSourceName)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MySQL: %w", err)
	}

	if err = db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping MySQL: %w", err)
	}

	adapter := &MySQLAdapter{db: db}
	if err := adapter.initSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize database schema: %w", err)
	}

	log.Println("Successfully connected to MySQL and initialized schema.")
	return adapter, nil
}

// initSchema 初始化数据库表结构
func (a *MySQLAdapter) initSchema() error {
	// 创建 resumes 表
	createResumesTableSQL := `
	CREATE TABLE IF NOT EXISTS resumes (
		id VARCHAR(255) PRIMARY KEY,
		candidate_name VARCHAR(255),
		candidate_phone VARCHAR(50),
		candidate_email VARCHAR(255),
		summary_skills TEXT,
		summary_total_experience VARCHAR(100),
		summary_education VARCHAR(100),
		original_file_path VARCHAR(1024),
		processed_text LONGTEXT,
		status VARCHAR(50) DEFAULT 'new',
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	);`
	_, err := a.db.Exec(createResumesTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create resumes table: %w", err)
	}

	// 创建 resume_chunks 表
	createResumeChunksTableSQL := `
	CREATE TABLE IF NOT EXISTS resume_chunks (
		id INT AUTO_INCREMENT PRIMARY KEY,
		resume_id VARCHAR(255) NOT NULL,
		chunk_sequential_id INT NOT NULL,
		chunk_type VARCHAR(100),
		content LONGTEXT,
		importance_score INT,
		metadata_skills TEXT,
		metadata_experience_years INT,
		metadata_education_level VARCHAR(100),
		vector_db_id VARCHAR(255),
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		FOREIGN KEY (resume_id) REFERENCES resumes(id) ON DELETE CASCADE,
		UNIQUE KEY resume_chunk_unique (resume_id, chunk_sequential_id)
	);`
	_, err = a.db.Exec(createResumeChunksTableSQL)
	if err != nil {
		return fmt.Errorf("failed to create resume_chunks table: %w", err)
	}
	return nil
}

// SaveResumeData 保存完整的简历数据到MySQL
func (a *MySQLAdapter) SaveResumeData(ctx context.Context, resumeData *types.ResumeData, originalFilePath string, processedText string) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	summarySkillsJSON, _ := json.Marshal(resumeData.Summary.Skills)

	// 保存到 resumes 表
	resumeInsertSQL := `
	INSERT INTO resumes (id, candidate_name, candidate_phone, candidate_email, 
				 summary_skills, summary_total_experience, summary_education, 
				 original_file_path, processed_text, status)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) 
	ON DUPLICATE KEY UPDATE 
		candidate_name = VALUES(candidate_name),
		candidate_phone = VALUES(candidate_phone),
		candidate_email = VALUES(candidate_email),
		summary_skills = VALUES(summary_skills),
		summary_total_experience = VALUES(summary_total_experience),
		summary_education = VALUES(summary_education),
		original_file_path = VALUES(original_file_path),
		processed_text = VALUES(processed_text),
		status = VALUES(status);` // Decide on status update logic for duplicates

	_, err = tx.ExecContext(ctx, resumeInsertSQL,
		resumeData.ResumeID,
		resumeData.CandidateInfo.Name,
		resumeData.CandidateInfo.Phone,
		resumeData.CandidateInfo.Email,
		string(summarySkillsJSON),
		resumeData.Summary.TotalExperience,
		resumeData.Summary.Education,
		originalFilePath,
		processedText,
		"chunked", // 初始状态，后续可以更新
	)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to insert or update resume: %w", err)
	}

	// 保存到 resume_chunks 表
	chunkInsertSQL := `
	INSERT INTO resume_chunks (resume_id, chunk_sequential_id, chunk_type, content, 
					 importance_score, metadata_skills, metadata_experience_years, metadata_education_level)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?) 
	ON DUPLICATE KEY UPDATE
		chunk_type = VALUES(chunk_type),
		content = VALUES(content),
		importance_score = VALUES(importance_score),
		metadata_skills = VALUES(metadata_skills),
		metadata_experience_years = VALUES(metadata_experience_years),
		metadata_education_level = VALUES(metadata_education_level);`

	stmt, err := tx.PrepareContext(ctx, chunkInsertSQL)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to prepare chunk insert statement: %w", err)
	}
	defer stmt.Close()

	for _, chunk := range resumeData.Chunks {
		chunkSkillsJSON, _ := json.Marshal(chunk.Metadata.Skills)
		_, err = stmt.ExecContext(ctx,
			resumeData.ResumeID,
			chunk.ChunkID, // This is chunk_sequential_id from LLM
			chunk.ChunkType,
			chunk.Content,
			chunk.ImportanceScore,
			string(chunkSkillsJSON),
			chunk.Metadata.ExperienceYears,
			chunk.Metadata.EducationLevel,
		)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to insert or update chunk for resume_id %s, chunk_id %d: %w", resumeData.ResumeID, chunk.ChunkID, err)
		}
	}

	if err = tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}
	return nil
}

// GetResumeByID 根据ID获取简历主要信息
func (a *MySQLAdapter) GetResumeByID(ctx context.Context, resumeID string) (*types.ResumeData, error) {
	// 这只是一个基本框架，需要填充 ResumeData 的所有字段
	// 你可能需要多次查询或 JOIN 来获取所有chunks等
	// 这里简化为只获取resumes表信息
	selectSQL := "SELECT id, candidate_name, candidate_phone, candidate_email, summary_skills, summary_total_experience, summary_education, processed_text, status FROM resumes WHERE id = ?"
	row := a.db.QueryRowContext(ctx, selectSQL, resumeID)

	var rd types.ResumeData
	var summarySkillsJSON string
	// 注意：这里只扫描了部分字段，需要根据ResumeData结构完善
	err := row.Scan(
		&rd.ResumeID,
		&rd.CandidateInfo.Name,
		&rd.CandidateInfo.Phone,
		&rd.CandidateInfo.Email,
		&summarySkillsJSON,
		&rd.Summary.TotalExperience,
		&rd.Summary.Education,
		&sql.NullString{}, // processed_text，这里简化处理，实际应读取
		&sql.NullString{}, // status，同上
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Or a specific error like ErrNotFound
		}
		return nil, fmt.Errorf("failed to get resume by ID %s: %w", resumeID, err)
	}
	if err := json.Unmarshal([]byte(summarySkillsJSON), &rd.Summary.Skills); err != nil {
		log.Printf("Warning: failed to unmarshal summary skills for resume %s: %v", resumeID, err)
	}

	// TODO: 获取并填充 Chunks 数据

	return &rd, nil
}

// UpdateResumeStatus 更新简历状态
func (a *MySQLAdapter) UpdateResumeStatus(ctx context.Context, resumeID string, status string) error {
	updateSQL := "UPDATE resumes SET status = ? WHERE id = ?"
	_, err := a.db.ExecContext(ctx, updateSQL, status, resumeID)
	if err != nil {
		return fmt.Errorf("failed to update status for resume %s: %w", resumeID, err)
	}
	return nil
}

// UpdateChunkVectorDBID 更新特定chunk的vector_db_id
func (a *MySQLAdapter) UpdateChunkVectorDBID(ctx context.Context, resumeID string, chunkSequentialID int, vectorDBID string) error {
	updateSQL := "UPDATE resume_chunks SET vector_db_id = ? WHERE resume_id = ? AND chunk_sequential_id = ?"
	_, err := a.db.ExecContext(ctx, updateSQL, vectorDBID, resumeID, chunkSequentialID)
	if err != nil {
		return fmt.Errorf("failed to update vector_db_id for resume %s, chunk %d: %w", resumeID, chunkSequentialID, err)
	}
	return nil
}

// GetChunksForEmbedding 获取需要生成嵌入的简历块 (例如，状态为 'chunked' 且 vector_db_id 为 NULL 的)
func (a *MySQLAdapter) GetChunksForEmbedding(ctx context.Context, resumeID string) ([]types.ResumeChunk, error) {
	selectSQL := `
	SELECT rc.chunk_sequential_id, rc.chunk_type, rc.content, rc.importance_score, 
	       rc.metadata_skills, rc.metadata_experience_years, rc.metadata_education_level, r.candidate_name, r.candidate_phone, r.candidate_email
	FROM resume_chunks rc
	JOIN resumes r ON rc.resume_id = r.id
	WHERE rc.resume_id = ? AND (rc.vector_db_id IS NULL OR rc.vector_db_id = '');` // 根据实际逻辑调整

	rows, err := a.db.QueryContext(ctx, selectSQL, resumeID)
	if err != nil {
		return nil, fmt.Errorf("failed to query chunks for embedding for resume %s: %w", resumeID, err)
	}
	defer rows.Close()

	var chunks []types.ResumeChunk
	for rows.Next() {
		var chunk types.ResumeChunk
		var skillsJSON string
		// 这些用于填充UniqueIdentifiers，即使它们在chunk级别可能重复
		var candName, candPhone, candEmail sql.NullString

		err := rows.Scan(
			&chunk.ChunkID, // 这是 chunk_sequential_id
			&chunk.ChunkType,
			&chunk.Content,
			&chunk.ImportanceScore,
			&skillsJSON,
			&chunk.Metadata.ExperienceYears,
			&chunk.Metadata.EducationLevel,
			&candName, &candPhone, &candEmail,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan chunk row: %w", err)
		}
		if err := json.Unmarshal([]byte(skillsJSON), &chunk.Metadata.Skills); err != nil {
			// Log or handle error if skills JSON is malformed
			log.Printf("Warning: could not unmarshal skills for chunk %d of resume %s: %v", chunk.ChunkID, resumeID, err)
		}
		chunk.UniqueIdentifiers.Name = candName.String
		chunk.UniqueIdentifiers.Phone = candPhone.String
		chunk.UniqueIdentifiers.Email = candEmail.String
		chunks = append(chunks, chunk)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("error after iterating chunk rows: %w", err)
	}
	return chunks, nil
}

// Close 关闭数据库连接
func (a *MySQLAdapter) Close() {
	if a.db != nil {
		a.db.Close()
	}
}
