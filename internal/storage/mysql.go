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
		&models.ResumeSubmission{},
		&models.ResumeSubmissionChunk{},
		&models.JobSubmissionMatch{},
		&models.Interviewer{},
		&models.InterviewEvaluation{},
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

// 泛型查询方法 - 通过ID获取记录
func (m *MySQL) GetByID(id interface{}, dest interface{}) error {
	return m.db.First(dest, "id = ?", id).Error
}

// 泛型查询方法 - 通过条件查询记录
func (m *MySQL) Find(dest interface{}, query interface{}, args ...interface{}) error {
	return m.db.Where(query, args...).Find(dest).Error
}

// 泛型创建/更新方法
func (m *MySQL) Save(value interface{}) error {
	return m.db.Save(value).Error
}

// 泛型删除方法
func (m *MySQL) Delete(value interface{}, query interface{}, args ...interface{}) error {
	return m.db.Where(query, args...).Delete(value).Error
}

// BatchInsertResumeSubmissions 批量插入简历提交记录
func (m *MySQL) BatchInsertResumeSubmissions(ctx context.Context, submissions []models.ResumeSubmission) error {
	if len(submissions) == 0 {
		return nil
	}
	return m.db.WithContext(ctx).Create(&submissions).Error
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
func (m *MySQL) SaveResumeChunks(tx *gorm.DB, submissionUUID string, sections []*types.ResumeSection) error {
	if len(sections) == 0 {
		return nil
	}
	chunks := make([]models.ResumeSubmissionChunk, len(sections))
	for i, section := range sections {
		chunks[i] = models.ResumeSubmissionChunk{
			SubmissionUUID:      submissionUUID,
			ChunkIDInSubmission: i + 1, // 从1开始的chunk_id
			ChunkType:           string(section.Type),
			ChunkTitle:          section.Title,
			ChunkContentText:    section.Content,
		}
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
