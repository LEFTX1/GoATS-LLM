package storage

import (
	"fmt"
	"log"

	"ai-agent-go/pkg/config"
	"ai-agent-go/pkg/storage/models" // 导入GORM模型

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MySQLAdapter 负责与MySQL数据库的交互
type MySQLAdapter struct {
	db  *gorm.DB // 使用 gorm.DB
	cfg *config.MySQLConfig
}

// NewMySQLAdapter 创建一个新的MySQLAdapter实例并初始化数据库连接和表结构
func NewMySQLAdapter(cfg *config.MySQLConfig) (*MySQLAdapter, error) {
	if cfg == nil {
		return nil, fmt.Errorf("MySQL配置不能为空")
	}

	// 构建DSN
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.Database)

	// 添加 DisableForeignKeyConstraintWhenMigrating 配置，禁用自动外键创建
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		DisableForeignKeyConstraintWhenMigrating: true, // 禁用自动外键创建以避免反向外键问题
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MySQL with GORM: %w", err)
	}

	adapter := &MySQLAdapter{
		db:  db,
		cfg: cfg,
	}

	// 使用 GORM 的 AutoMigrate 功能自动迁移表结构
	if err := adapter.autoMigrateSchema(); err != nil {
		sqlDB, _ := db.DB() // 尝试获取底层 *sql.DB 以关闭
		if sqlDB != nil {
			sqlDB.Close()
		}
		return nil, fmt.Errorf("failed to auto-migrate database schema with GORM: %w", err)
	}

	// 可选：在此处手动创建外键约束
	// 注意：由于禁用了自动外键创建，您可能需要在此处手动添加所需的外键约束
	// 这可以通过执行原始SQL语句完成，例如:
	// a.db.Exec("ALTER TABLE resume_submissions ADD CONSTRAINT fk_rs_candidate_id FOREIGN KEY (candidate_id) REFERENCES candidates(candidate_id) ON DELETE SET NULL")

	log.Println("Successfully connected to MySQL and auto-migrated schema with GORM.")
	return adapter, nil
}

// autoMigrateSchema 使用GORM自动迁移数据库表结构
func (a *MySQLAdapter) autoMigrateSchema() error {
	// 列出所有需要迁移的模型
	err := a.db.AutoMigrate(
		&models.Candidate{},
		&models.Job{},
		&models.ResumeSubmission{},
		&models.ResumeSubmissionChunk{},
		&models.JobSubmissionMatch{},
		&models.Interviewer{},
		&models.InterviewEvaluation{},
	)
	if err != nil {
		return fmt.Errorf("GORM AutoMigrate failed: %w", err)
	}
	log.Println("GORM schema auto-migration successful.")
	return nil
}

// DB 返回GORM数据库连接实例
func (a *MySQLAdapter) DB() *gorm.DB {
	return a.db
}

// Close 关闭数据库连接
func (a *MySQLAdapter) Close() error {
	sqlDB, err := a.db.DB()
	if err != nil {
		return fmt.Errorf("failed to get underlying sql.DB from GORM: %w", err)
	}
	return sqlDB.Close()
}
