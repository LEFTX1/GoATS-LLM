package storage

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"context"
	"fmt"
	"log"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

// Database 关系数据库接口
type Database interface {
	// DB 返回GORM数据库连接实例
	DB() *gorm.DB

	// Close 关闭数据库连接
	Close() error

	// GetByID 通过ID获取记录
	GetByID(id interface{}, dest interface{}) error

	// Find 通过条件查询记录
	Find(dest interface{}, query interface{}, args ...interface{}) error

	// Save 保存/更新记录
	Save(value interface{}) error

	// Delete 删除记录
	Delete(value interface{}, query interface{}, args ...interface{}) error
}

// 确保MySQL实现了Database接口
var _ Database = (*MySQL)(nil)

// MySQL 提供关系数据库功能
type MySQL struct {
	db  *gorm.DB
	cfg *config.MySQLConfig
}

// NewMySQL 创建MySQL客户端
func NewMySQL(cfg *config.MySQLConfig) (*MySQL, error) {
	if cfg == nil {
		return nil, fmt.Errorf("MySQL配置不能为空")
	}

	// 构建DSN，添加超时设置
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&timeout=%ds&readTimeout=%ds&writeTimeout=%ds",
		cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.Database,
		cfg.ConnectTimeoutSeconds, cfg.ReadTimeoutSeconds, cfg.WriteTimeoutSeconds)

	// 配置GORM日志级别
	var logLevel logger.LogLevel
	switch cfg.LogLevel {
	case 1:
		logLevel = logger.Silent
	case 2:
		logLevel = logger.Error
	case 3:
		logLevel = logger.Warn
	case 4:
		logLevel = logger.Info
	default:
		logLevel = logger.Info // 默认Info级别
	}

	// GORM配置增强
	gormConfig := &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true,                             // 禁用自动外键创建
		Logger:                                   logger.Default.LogMode(logLevel), // 设置日志级别
		PrepareStmt:                              true,                             // 开启预编译语句缓存
	}

	db, err := gorm.Open(mysql.Open(dsn), gormConfig)
	if err != nil {
		return nil, fmt.Errorf("连接MySQL失败: %w", err)
	}

	// 设置连接池参数
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}

	// 设置连接池参数
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)                                           // 最大空闲连接数
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)                                           // 最大打开连接数
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeMinutes) * time.Minute) // 连接最大生命周期
	sqlDB.SetConnMaxIdleTime(time.Duration(cfg.ConnMaxIdleTimeMinutes) * time.Minute) // 空闲连接最大生命周期

	m := &MySQL{
		db:  db,
		cfg: cfg,
	}

	// 使用 GORM 的 AutoMigrate 功能自动迁移表结构
	if err := m.autoMigrateSchema(); err != nil {
		sqlDB, _ := db.DB() // 尝试获取底层 *sql.DB 以关闭
		if sqlDB != nil {
			sqlDB.Close()
		}
		return nil, fmt.Errorf("自动迁移数据库结构失败: %w", err)
	}

	log.Println("成功连接到MySQL并自动迁移数据库结构")
	return m, nil
}

// autoMigrateSchema 使用GORM自动迁移数据库表结构
func (m *MySQL) autoMigrateSchema() error {
	// 列出所有需要迁移的模型
	err := m.db.AutoMigrate(
		&models.Candidate{},
		&models.Job{},
		&models.JobVector{},
		&models.ResumeSubmission{},
		&models.ResumeSubmissionChunk{},
		&models.JobSubmissionMatch{},
		&models.Interviewer{},
		&models.InterviewEvaluation{},
		&models.ReviewedResume{},
	)
	if err != nil {
		return fmt.Errorf("GORM自动迁移失败: %w", err)
	}
	log.Println("GORM数据库结构迁移成功")
	return nil
}

// DB 返回GORM数据库连接实例
func (m *MySQL) DB() *gorm.DB {
	return m.db
}

// Close 关闭数据库连接
func (m *MySQL) Close() error {
	sqlDB, err := m.db.DB()
	if err != nil {
		return fmt.Errorf("获取底层 sql.DB 失败: %w", err)
	}
	return sqlDB.Close()
}

// GetByID 泛型查询方法 - 通过ID获取记录
func (m *MySQL) GetByID(id interface{}, dest interface{}) error {
	return m.db.First(dest, "id = ?", id).Error
}

// Find 泛型查询方法 - 通过条件查询记录
func (m *MySQL) Find(dest interface{}, query interface{}, args ...interface{}) error {
	return m.db.Where(query, args...).Find(dest).Error
}

// Save 泛型创建/更新方法
func (m *MySQL) Save(value interface{}) error {
	return m.db.Save(value).Error
}

// Delete 泛型删除方法
func (m *MySQL) Delete(value interface{}, query interface{}, args ...interface{}) error {
	return m.db.Where(query, args...).Delete(value).Error
}

// BatchInsertResumeSubmissions 批量插入简历提交记录
func (m *MySQL) BatchInsertResumeSubmissions(ctx context.Context, submissions []models.ResumeSubmission) error {
	if len(submissions) == 0 {
		return nil
	}

	// 使用 GORM 的 Clauses 方法添加 ON DUPLICATE KEY UPDATE 子句
	// 当遇到主键冲突时，更新一个字段为其自身的值，实现幂等操作
	return m.db.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "submission_uuid"}},            // 主键列
			DoUpdates: clause.AssignmentColumns([]string{"submission_uuid"}), // 执行无实际意义的更新
		}).Create(&submissions).Error
}

// UpdateResumeProcessingStatus 更新简历处理状态
func (m *MySQL) UpdateResumeProcessingStatus(ctx context.Context, submissionUUID string, status string) error {
	return m.db.WithContext(ctx).Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", submissionUUID).Update("processing_status", status).Error
}

// UpdateResumeRawTextMD5 更新简历的原始文本MD5
func (m *MySQL) UpdateResumeRawTextMD5(ctx context.Context, submissionUUID string, rawTextMD5 string) error {
	if rawTextMD5 == "" {
		return nil // 如果MD5为空，则不更新
	}
	return m.db.WithContext(ctx).Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", submissionUUID).Update("raw_text_md5", rawTextMD5).Error
}

// SaveResumeChunks 保存简历分块信息 (在事务中执行)
func (m *MySQL) SaveResumeChunks(tx *gorm.DB, submissionUUID string, sections []*types.ResumeSection, pointIDs []string) error {
	if len(sections) == 0 {
		return nil
	}
	// 安全检查: 确保 pointIDs 长度与 sections 一致 (如果提供了 pointIDs)
	if len(pointIDs) > 0 && len(sections) != len(pointIDs) {
		return fmt.Errorf("sections and pointIDs length mismatch: %d != %d", len(sections), len(pointIDs))
	}

	chunks := make([]models.ResumeSubmissionChunk, len(sections))
	for i, section := range sections {
		chunk := models.ResumeSubmissionChunk{
			SubmissionUUID:      submissionUUID,
			ChunkIDInSubmission: section.ChunkID, // 使用LLM生成的ChunkID
			ChunkType:           string(section.Type),
			ChunkTitle:          section.Title,
			ChunkContentText:    section.Content,
		}
		// 如果提供了PointID并且长度匹配，则赋值
		if len(pointIDs) > 0 {
			if pointIDs[i] != "" {
				chunk.PointID = &pointIDs[i]
			}
		}
		chunks[i] = chunk
	}
	return tx.Create(&chunks).Error
}

// SaveResumeBasicInfo 保存简历基本信息 (在事务中执行)
func (m *MySQL) SaveResumeBasicInfo(tx *gorm.DB, submissionUUID string, metadata map[string]string) error {
	basicInfoJSON, err := models.StringMapToJSON(metadata)
	if err != nil {
		return fmt.Errorf("转换metadata为JSON失败: %w", err)
	}

	var identifier string
	if name, hasName := metadata["name"]; hasName {
		if phone, hasPhone := metadata["phone"]; hasPhone {
			identifier = fmt.Sprintf("%s_%s", name, phone)
		} else {
			identifier = fmt.Sprintf("%s_", name)
		}
	}

	updates := map[string]interface{}{
		"llm_parsed_basic_info": basicInfoJSON,
		"llm_resume_identifier": identifier,
	}
	return tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", submissionUUID).Updates(updates).Error
}

// UpdateResumeSubmissionFields 更新 ResumeSubmission 表的多个字段 (在事务中执行)
func (m *MySQL) UpdateResumeSubmissionFields(tx *gorm.DB, submissionUUID string, updates map[string]interface{}) error {
	if len(updates) == 0 {
		return nil
	}
	return tx.Model(&models.ResumeSubmission{}).Where("submission_uuid = ?", submissionUUID).Updates(updates).Error
}

// CreateJobSubmissionMatch 创建 JobSubmissionMatch 记录 (在事务中执行)
func (m *MySQL) CreateJobSubmissionMatch(tx *gorm.DB, match *models.JobSubmissionMatch) error {
	return tx.Create(match).Error
}

// GetJobByID 通过 JobID 获取 Job 记录
func (m *MySQL) GetJobByID(db *gorm.DB, jobID string) (*models.Job, error) {
	var job models.Job
	if err := db.Where("job_id = ?", jobID).First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// GetJobVectorByID 通过 JobID 获取 JobVector 记录
func (m *MySQL) GetJobVectorByID(db *gorm.DB, jobID string) (*models.JobVector, error) {
	var jobVector models.JobVector
	if err := db.Where("job_id = ?", jobID).First(&jobVector).Error; err != nil {
		return nil, err
	}
	return &jobVector, nil
}

// CreateJob 创建一个新的Job记录 (如果需要独立的创建方法)
func (m *MySQL) CreateJob(tx *gorm.DB, job *models.Job) error {
	return tx.Create(job).Error
}

// UpdateJob 更新一个已有的Job记录 (如果需要独立的更新方法)
func (m *MySQL) UpdateJob(tx *gorm.DB, job *models.Job) error {
	return tx.Save(job).Error // Save 会基于主键更新或创建
}

// CreateJobVector 创建一个新的JobVector记录
func (m *MySQL) CreateJobVector(tx *gorm.DB, jobVector *models.JobVector) error {
	return tx.Create(jobVector).Error
}

// UpdateJobVector 更新一个已有的JobVector记录
func (m *MySQL) UpdateJobVector(tx *gorm.DB, jobVector *models.JobVector) error {
	return tx.Save(jobVector).Error // Save 会基于主键更新或创建
}

// UpdateResumeChunkPointIDs 批量更新 ResumeSubmissionChunk 记录的 PointID
func (m *MySQL) UpdateResumeChunkPointIDs(ctx context.Context, submissionUUID string, chunkDBIDs []uint64, pointIDs []string) error {
	if len(chunkDBIDs) != len(pointIDs) {
		return fmt.Errorf("chunkDBIDs 和 pointIDs 长度不匹配: %d != %d", len(chunkDBIDs), len(pointIDs))
	}
	if len(chunkDBIDs) == 0 {
		return nil // 没有需要更新的记录
	}

	tx := m.DB().WithContext(ctx).Begin()
	if tx.Error != nil {
		return fmt.Errorf("开始事务失败: %w", tx.Error)
	}
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
			// 可以选择记录 panic
		}
	}()

	for i, chunkDBID := range chunkDBIDs {
		pointID := pointIDs[i]
		result := tx.Model(&models.ResumeSubmissionChunk{}).Where("chunk_db_id = ? AND submission_uuid = ?", chunkDBID, submissionUUID).Update("point_id", &pointID) // 使用 &pointID 确保可以更新为 NULL（如果 pointID 是空字符串，则更新为 NULL，如果pointID是指针，则传递指针）
		if result.Error != nil {
			tx.Rollback()
			return fmt.Errorf("更新 chunk_db_id %d 的 point_id 失败: %w", chunkDBID, result.Error)
		}
		if result.RowsAffected == 0 {
			// 可以选择性地处理未找到记录的情况，例如记录日志或返回错误
			// logger.Ctx(ctx).Warn().Uint64("chunkDBID", chunkDBID).Str("submissionUUID", submissionUUID).Msg("UpdateResumeChunkPointIDs: 未找到要更新的记录或point_id未改变")
		}
	}

	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}

	return nil
}
