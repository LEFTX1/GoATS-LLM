package storage

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/storage/models"
	"ai-agent-go/internal/types"
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/gofrs/uuid/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
	"go.opentelemetry.io/otel/trace"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

var mysqlTracer = otel.Tracer("ai-agent-go/storage/mysql")

// GormTracingPlugin 是一个GORM插件，用于向OpenTelemetry中添加数据库操作的追踪点
type GormTracingPlugin struct {
	tracer         trace.Tracer
	dbName         string
	dbSystem       string
	disableErrSkip bool
}

// Name 返回插件名称
func (p *GormTracingPlugin) Name() string {
	return "GormOpenTelemetryPlugin"
}

// Initialize 注册GORM回调以启用追踪
func (p *GormTracingPlugin) Initialize(db *gorm.DB) error {
	// 为各种操作类型注册回调
	cb := db.Callback()

	// 为所有CRUD操作注册Before和After回调
	if err := cb.Create().Before("gorm:create").Register("otel:before_create", p.before("CREATE")); err != nil {
		return err
	}
	if err := cb.Create().After("gorm:create").Register("otel:after_create", p.after()); err != nil {
		return err
	}

	if err := cb.Query().Before("gorm:query").Register("otel:before_query", p.before("SELECT")); err != nil {
		return err
	}
	if err := cb.Query().After("gorm:query").Register("otel:after_query", p.after()); err != nil {
		return err
	}

	if err := cb.Update().Before("gorm:update").Register("otel:before_update", p.before("UPDATE")); err != nil {
		return err
	}
	if err := cb.Update().After("gorm:update").Register("otel:after_update", p.after()); err != nil {
		return err
	}

	if err := cb.Delete().Before("gorm:delete").Register("otel:before_delete", p.before("DELETE")); err != nil {
		return err
	}
	if err := cb.Delete().After("gorm:delete").Register("otel:after_delete", p.after()); err != nil {
		return err
	}

	if err := cb.Row().Before("gorm:row").Register("otel:before_row", p.before("ROW")); err != nil {
		return err
	}
	if err := cb.Row().After("gorm:row").Register("otel:after_row", p.after()); err != nil {
		return err
	}

	if err := cb.Raw().Before("gorm:raw").Register("otel:before_raw", p.before("RAW")); err != nil {
		return err
	}
	if err := cb.Raw().After("gorm:raw").Register("otel:after_raw", p.after()); err != nil {
		return err
	}

	return nil
}

// before 返回在GORM操作之前执行的回调函数
func (p *GormTracingPlugin) before(operation string) func(db *gorm.DB) {
	return func(db *gorm.DB) {
		// 如果是错误跳过且DisableErrSkip为true，则跳过追踪
		if p.disableErrSkip && db.Statement.SkipHooks {
			return
		}

		// 从DB获取上下文
		ctx := db.Statement.Context
		if ctx == nil {
			ctx = context.Background()
		}

		// 获取操作表名，如果为空则使用"unknown"
		tableName := db.Statement.Table
		if tableName == "" {
			tableName = "unknown"
		}

		// 创建一个新的span
		spanName := fmt.Sprintf("%s %s", operation, tableName)
		opts := []trace.SpanStartOption{
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				semconv.DBSystemMySQL,
				attribute.String("db.name", p.dbName),
				attribute.String("db.operation", operation),
				attribute.String("db.sql.table", tableName),
			),
		}

		// 获取SQL语句（如果有）
		sqlStatement := db.Statement.SQL.String()
		if sqlStatement != "" {
			opts = append(opts, trace.WithAttributes(
				attribute.String("db.statement", sqlStatement),
			))
		}

		newCtx, span := p.tracer.Start(ctx, spanName, opts...)

		// 将span保存在DB上下文中，以便在after回调中使用
		db.Statement.Context = context.WithValue(newCtx, "otel-span", span)
	}
}

// after 返回在GORM操作之后执行的回调函数
func (p *GormTracingPlugin) after() func(db *gorm.DB) {
	return func(db *gorm.DB) {
		// 从DB上下文中获取span
		span, ok := db.Statement.Context.Value("otel-span").(trace.Span)
		if !ok {
			return
		}
		defer span.End()

		// 添加额外的属性
		if db.Statement.RowsAffected > 0 {
			span.SetAttributes(attribute.Int64("db.rows_affected", db.Statement.RowsAffected))
		} else {
			span.SetAttributes(attribute.Int64("db.rows_affected", 0))
		}

		// 记录错误（如果有），但正确处理ErrRecordNotFound
		if db.Error != nil {
			if db.Error == gorm.ErrRecordNotFound {
				// ErrRecordNotFound 是业务逻辑正常情况的一部分，不应作为错误处理
				span.SetAttributes(attribute.String("error.type", "record_not_found"))
				span.SetStatus(codes.Ok, "record not found")
			} else {
				// 真正的错误情况
				span.SetAttributes(attribute.String("error.type", "database_error"))
				span.SetAttributes(attribute.String("error.message", db.Error.Error()))
				span.RecordError(db.Error)
				span.SetStatus(codes.Error, db.Error.Error())
			}
		} else {
			span.SetStatus(codes.Ok, "")
		}
	}
}

// NewGormTracingPlugin 创建一个新的GORM追踪插件
func NewGormTracingPlugin(dbName string) *GormTracingPlugin {
	return &GormTracingPlugin{
		tracer:         mysqlTracer,
		dbName:         dbName,
		dbSystem:       "mysql",
		disableErrSkip: true, // 默认禁用错误跳过，减少误报错误
	}
}

// WithDisableErrSkip 设置是否禁用错误跳过
func (p *GormTracingPlugin) WithDisableErrSkip(disable bool) *GormTracingPlugin {
	p.disableErrSkip = disable
	return p
}

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
		NowFunc: func() time.Time {
			return time.Now().Local() // 使用本地时间作为默认时间
		},
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

	// 注册OpenTelemetry追踪插件
	tracingPlugin := NewGormTracingPlugin(cfg.Database).WithDisableErrSkip(true)
	if err := db.Use(tracingPlugin); err != nil {
		return nil, fmt.Errorf("注册追踪插件失败: %w", err)
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
	// 保存当前的日志级别
	currentLogger := m.db.Logger

	// 创建一个静默的logger以关闭SQL日志打印
	silentLogger := logger.New(
		log.New(log.Writer(), "", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Silent, // 设置为Silent级别，关闭所有SQL日志
			IgnoreRecordNotFoundError: true,
			Colorful:                  false,
		},
	)

	// 创建一个使用静默日志记录器的DB会话
	silentDB := m.db.Session(&gorm.Session{Logger: silentLogger})

	// 列出所有需要迁移的模型
	err := silentDB.AutoMigrate(
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

	// 恢复原来的日志记录器
	m.db = m.db.Session(&gorm.Session{Logger: currentLogger})

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
	// 创建一个命名span
	ctx, span := mysqlTracer.Start(ctx, "MySQL.BatchInsertResumeSubmissions",
		trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()

	span.SetAttributes(
		semconv.DBSystemMySQL,
		attribute.String("db.name", m.cfg.Database),
		attribute.String("db.operation", "INSERT_ON_DUPLICATE"),
		attribute.String("db.sql.table", "resume_submissions"),
		attribute.Int("batch.size", len(submissions)),
	)

	if len(submissions) == 0 {
		span.SetStatus(codes.Ok, "no submissions to insert")
		return nil
	}

	// 使用 GORM 的 Clauses 方法添加 ON DUPLICATE KEY UPDATE 子句
	// 当遇到主键冲突时，更新一个字段为其自身的值，实现幂等操作
	err := m.db.WithContext(ctx).Clauses(
		clause.OnConflict{
			Columns:   []clause.Column{{Name: "submission_uuid"}},            // 主键列
			DoUpdates: clause.AssignmentColumns([]string{"submission_uuid"}), // 执行无实际意义的更新
		}).Create(&submissions).Error

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	span.SetAttributes(attribute.Int("db.rows_affected", len(submissions)))
	span.SetStatus(codes.Ok, "")
	return nil
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

// FindOrCreateCandidate 查找或创建候选人
// 它首先尝试通过邮箱或电话号码查找候选人。如果找到，则返回该候选人。
// 如果未找到，则创建一个新的候选人记录并返回。
// 使用 GORM 事务以确保操作的原子性。
func (m *MySQL) FindOrCreateCandidate(ctx context.Context, tx *gorm.DB, basicInfo map[string]string) (*models.Candidate, error) {
	email, _ := basicInfo["email"]
	phone, _ := basicInfo["phone"]

	ctx, span := mysqlTracer.Start(ctx, "FindOrCreateCandidate", trace.WithAttributes(
		attribute.String("candidate.email", email),
		attribute.String("candidate.phone", phone),
	))
	defer span.End()

	// 确保至少有一个有效标识符
	if email == "" && phone == "" {
		err := fmt.Errorf("邮箱和电话至少需要一个")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	var candidate models.Candidate
	db := m.db
	if tx != nil {
		db = tx // 如果在事务中，使用事务的 db handle
	}

	// 1. 尝试通过邮箱或电话查找候选人
	var conditions []string
	var args []interface{}
	if email != "" {
		conditions = append(conditions, "primary_email = ?")
		args = append(args, email)
	}
	if phone != "" {
		conditions = append(conditions, "primary_phone = ?")
		args = append(args, phone)
	}

	// 使用 GORM 的 Or 构建查询
	var firstCondition string
	if len(conditions) > 0 {
		firstCondition = conditions[0]
		conditions = conditions[1:]
	} else {
		// 如果 email 和 phone 都为空，则直接返回错误，避免下面的 orQuery panic
		err := fmt.Errorf("邮箱和电话都为空")
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}

	orQuery := db.WithContext(ctx).Model(&models.Candidate{}).Where(firstCondition, args[0])
	for i, cond := range conditions {
		orQuery = orQuery.Or(cond, args[i+1])
	}

	err := orQuery.First(&candidate).Error

	if err == nil {
		// 找到了候选人，可以考虑在这里用 basicInfo 更新现有记录
		span.SetAttributes(attribute.Bool("candidate.found", true), attribute.String("candidate.id", candidate.CandidateID))
		return &candidate, nil
	}

	if !errors.Is(err, gorm.ErrRecordNotFound) {
		// 发生了除"未找到记录"之外的其它数据库错误
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to query candidate")
		return nil, fmt.Errorf("查询候选人失败: %w", err)
	}

	// 2. 如果未找到，创建新的候选人
	span.SetAttributes(attribute.Bool("candidate.found", false))

	newUUID, err := uuid.NewV7()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to generate UUIDv7")
		return nil, fmt.Errorf("生成UUIDv7失败: %w", err)
	}

	gender, ok := basicInfo["gender"]
	if !ok || gender == "" {
		gender = "未知" // 设置默认值
	}

	newCandidate := &models.Candidate{
		CandidateID:     newUUID.String(),
		PrimaryName:     basicInfo["name"],
		PrimaryEmail:    email,
		PrimaryPhone:    phone,
		Gender:          gender,
		CurrentLocation: basicInfo["current_location"],
		// 其他字段可以在后续步骤中更新，比如 profile_summary
	}

	if err := db.WithContext(ctx).Create(newCandidate).Error; err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "failed to create candidate")
		return nil, fmt.Errorf("创建新候选人失败: %w", err)
	}

	span.SetAttributes(attribute.String("candidate.id", newCandidate.CandidateID))
	return newCandidate, nil
}
