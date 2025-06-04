package storage

import (
	"ai-agent-go/internal/config"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

// Storage 存储管理器，聚合所有存储相关依赖
type Storage struct {
	// 对象存储
	MinIO *MinIO

	// 消息队列
	RabbitMQ *RabbitMQ

	// 向量数据库
	Qdrant *Qdrant

	// 关系型数据库
	MySQL *MySQL

	// 键值存储
	Redis *Redis
}

// Config 存储配置
type Config struct {
	MinIOConfig    *config.MinIOConfig
	RabbitMQConfig *config.RabbitMQConfig
	MySQLConfig    *config.MySQLConfig
	RedisConfig    *config.RedisConfig
}

// NewStorage 创建存储管理器
func NewStorage(ctx context.Context, cfg *config.Config) (*Storage, error) {
	if cfg == nil {
		return nil, fmt.Errorf("配置不能为空")
	}

	storage := &Storage{}
	var err error
	var initErrors []string

	// 根据配置决定 MinIO 的 logger
	var minioLogger *log.Logger
	if cfg.Logger.Level == "debug" || (cfg.MinIO.EnableTestLogging) {
		minioLogger = log.New(os.Stderr, "[MinIOStorage] ", log.LstdFlags|log.Lshortfile)
	} else {
		minioLogger = log.New(io.Discard, "", 0)
	}

	// 初始化MinIO
	storage.MinIO, err = NewMinIO(&cfg.MinIO, minioLogger)
	if err != nil {
		log.Printf("警告: 初始化MinIO失败: %v", err)
		initErrors = append(initErrors, fmt.Sprintf("MinIO: %v", err))
	} else {
		log.Println("MinIO客户端初始化成功")
	}

	// 初始化RabbitMQ（如果配置了）
	if cfg.RabbitMQ.URL != "" {
		log.Printf("初始化RabbitMQ...")
		storage.RabbitMQ, err = NewRabbitMQ(&cfg.RabbitMQ)
		if err != nil {
			log.Printf("警告: 初始化RabbitMQ失败: %v", err)
			initErrors = append(initErrors, fmt.Sprintf("RabbitMQ: %v", err))
		}
	}

	// 初始化Qdrant（如果配置了）
	if cfg.Qdrant.Endpoint != "" {
		log.Printf("初始化Qdrant...")
		qdrantCfg := &config.QdrantConfig{
			Endpoint:   cfg.Qdrant.Endpoint,
			Collection: cfg.Qdrant.Collection,
			Dimension:  cfg.Qdrant.Dimension,
		}
		storage.Qdrant, err = NewQdrant(qdrantCfg)
		if err != nil {
			log.Printf("警告: 初始化Qdrant失败: %v", err)
			initErrors = append(initErrors, fmt.Sprintf("Qdrant: %v", err))
		}
	}

	// 初始化MySQL（如果配置了）
	if cfg.MySQL.Host != "" {
		log.Printf("初始化MySQL...")
		storage.MySQL, err = NewMySQL(&cfg.MySQL)
		if err != nil {
			log.Printf("警告: 初始化MySQL失败: %v", err)
			initErrors = append(initErrors, fmt.Sprintf("MySQL: %v", err))
		}
	}

	// 初始化Redis (如果配置了)
	if cfg.Redis.Address != "" { // Check if Redis address is configured in the main config
		log.Printf("初始化Redis at %s...", cfg.Redis.Address)
		// Pass the RedisConfig directly from the main config
		storage.Redis, err = NewRedisAdapter(&cfg.Redis) // Changed NewRedis to NewRedisAdapter to avoid conflict
		if err != nil {
			log.Printf("警告: 初始化Redis失败: %v", err)
			initErrors = append(initErrors, fmt.Sprintf("Redis: %v", err))
		}
	} else {
		log.Printf("Redis未配置, 跳过初始化.")
	}

	// 检查是否所有组件都初始化失败
	if storage.MinIO == nil && storage.RabbitMQ == nil && storage.Qdrant == nil && storage.MySQL == nil && storage.Redis == nil {
		return nil, fmt.Errorf("所有存储组件初始化失败: %s", strings.Join(initErrors, "; "))
	}

	// 如果至少有一个组件初始化成功，则返回存储管理器
	if len(initErrors) > 0 {
		log.Printf("警告: 以下存储组件初始化失败: %s", strings.Join(initErrors, "; "))
	}

	return storage, nil
}

// Close 关闭所有连接
func (s *Storage) Close() {
	// 关闭RabbitMQ连接
	if s.RabbitMQ != nil {
		if err := s.RabbitMQ.Close(); err != nil {
			log.Printf("关闭RabbitMQ连接失败: %v", err)
		}
	}

	// 关闭MySQL连接
	if s.MySQL != nil {
		if err := s.MySQL.Close(); err != nil {
			log.Printf("关闭MySQL连接失败: %v", err)
		}
	}

	// 关闭Redis连接
	if s.Redis != nil {
		if err := s.Redis.Close(); err != nil {
			log.Printf("关闭Redis连接失败: %v", err)
		}
	}
	// Qdrant and MinIO clients typically don't require an explicit Close(),
	// or their close is handled differently (e.g. Qdrant's gRPC client conn.Close())
	// Review their specific SDKs if explicit closing is needed.
}
