package config

import (
	"fmt"
	// "io/ioutil" // 已弃用
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// RedisConfig holds configuration for Redis
type RedisConfig struct {
	Address  string `yaml:"address"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
	// 连接池设置
	PoolSize     int `yaml:"pool_size"`      // 连接池大小
	MinIdleConns int `yaml:"min_idle_conns"` // 最小空闲连接数
	// 超时设置
	DialTimeoutSeconds  int `yaml:"dial_timeout_seconds"`  // 连接超时(秒)
	ReadTimeoutSeconds  int `yaml:"read_timeout_seconds"`  // 读取超时(秒)
	WriteTimeoutSeconds int `yaml:"write_timeout_seconds"` // 写入超时(秒)
	// 重试设置
	MaxRetries        int `yaml:"max_retries"`          // 最大重试次数
	MinRetryBackoffMS int `yaml:"min_retry_backoff_ms"` // 最小重试间隔(毫秒)
	MaxRetryBackoffMS int `yaml:"max_retry_backoff_ms"` // 最大重试间隔(毫秒)
	// 连接生命周期
	ConnMaxLifetimeMinutes int `yaml:"conn_max_lifetime_minutes"`  // 连接最大生命周期(分钟)
	ConnMaxIdleTimeMinutes int `yaml:"conn_max_idle_time_minutes"` // 空闲连接最大生命周期(分钟)
	// MD5记录过期时间(天)
	MD5RecordExpireDays int `yaml:"md5_record_expire_days"` // MD5记录过期时间(天)
}

// Config 应用程序配置
type Config struct {
	Aliyun struct {
		APIKey     string            `yaml:"api_key"`
		APIURL     string            `yaml:"api_url"`
		Model      string            `yaml:"model"`
		TaskModels map[string]string `yaml:"task_models"` // 任务专用模型
		Embedding  EmbeddingConfig   `yaml:"embedding"`   // Embedding specific config
	} `yaml:"aliyun"`

	Qdrant struct {
		Endpoint           string `yaml:"endpoint"`
		Collection         string `yaml:"collection"`
		Dimension          int    `yaml:"dimension"`
		APIKey             string `yaml:"api_key,omitempty"`    // 可选的API Key
		DefaultSearchLimit int    `yaml:"default_search_limit"` // 新增：默认搜索结果数量
	} `yaml:"qdrant"`

	// Tika服务器配置
	Tika TikaConfig `yaml:"tika"`

	// 新增RabbitMQ配置
	RabbitMQ RabbitMQConfig `yaml:"rabbitmq"`

	// 新增MinIO配置
	MinIO MinIOConfig `yaml:"minio"`

	// 新增MySQL配置
	MySQL MySQLConfig `yaml:"mysql"`

	// 新增Redis配置
	Redis RedisConfig `yaml:"redis"`

	// 新增服务器配置
	Server ServerConfig `yaml:"server"`

	// 新增LLM解析器配置
	LLMParser LLMParserConfig `yaml:"llm_parser"`

	// 新增JD评估器配置
	JobEvaluator JobEvaluatorConfig `yaml:"job_evaluator"`

	// 新增日志配置
	Logger LoggerConfig `yaml:"logger"`

	// 新增模型QPM限制配置
	ModelQPMLimits map[string]int `yaml:"model_qpm_limits"`

	// 新增用于记录当前处理流程主要解析器版本的字段
	ActiveParserVersion string `yaml:"active_parser_version"`
}

// EmbeddingConfig Aliyun Embedding specific configuration
type EmbeddingConfig struct {
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions"`
	BaseURL    string `yaml:"base_url"`
	// APIKey can be added here if it's different from the main Aliyun APIKey
	// APIKey     string `yaml:"api_key,omitempty"`
}

// TikaConfig Tika服务器配置结构
type TikaConfig struct {
	ServerURL    string `yaml:"server_url"`      // Tika服务器URL
	Timeout      int    `yaml:"timeout_seconds"` // 超时时间(秒)
	Type         string `yaml:"type"`            // 解析器类型，例如 "tika"
	MetadataMode string `yaml:"metadata_mode"`   // 元数据模式: "full", "minimal", "none"
}

// RabbitMQConfig RabbitMQ配置结构
type RabbitMQConfig struct {
	URL                      string `yaml:"url"` // 例如 "amqp://guest:guest@localhost:5672/"
	Host                     string `yaml:"host"`
	Port                     int    `yaml:"port"`
	Username                 string `yaml:"username"`
	Password                 string `yaml:"password"`
	VHost                    string `yaml:"vhost"`
	ResumeEventsExchange     string `yaml:"resume_events_exchange"`
	ProcessingEventsExchange string `yaml:"processing_events_exchange"`
	UploadedRoutingKey       string `yaml:"uploaded_routing_key"`
	ParsedRoutingKey         string `yaml:"parsed_routing_key"`
	MatchEventsExchange      string `yaml:"match_events_exchange"`
	MatchNeededRoutingKey    string `yaml:"match_needed_routing_key"`
	RawResumeQueue           string `yaml:"raw_resume_queue"`
	LLMParsingQueue          string `yaml:"llm_parsing_queue"`
	JobMatchingQueue         string `yaml:"job_matching_queue"`
	QdrantVectorizeQueue     string `yaml:"qdrant_vectorize_queue,omitempty"` // 新增: Qdrant向量化队列
	VectorizeRoutingKey      string `yaml:"vectorize_routing_key,omitempty"`  // 新增: Qdrant向量化路由键
	PrefetchCount            int    `yaml:"prefetch_count"`
	RetryInterval            string `yaml:"retry_interval"`
	MaxRetries               int    `yaml:"max_retries"`
	// 新增消费者工作线程和批量处理超时配置
	ConsumerWorkers map[string]int    `yaml:"consumer_workers"` // 例如: {"upload_consumer_workers": 5, "llm_consumer_workers": 3}
	BatchTimeouts   map[string]string `yaml:"batch_timeouts"`   // 例如: {"upload_batch_timeout": "10s"}
}

// MinIOConfig MinIO配置结构
type MinIOConfig struct {
	Endpoint        string `yaml:"endpoint"`
	AccessKeyID     string `yaml:"accessKeyID"`
	SecretAccessKey string `yaml:"secretAccessKey"`
	UseSSL          bool   `yaml:"useSSL"`
	BucketName      string `yaml:"bucketName"`
	Location        string `yaml:"location"` // 可选，存储桶区域
	// 新增对象存储桶名称
	OriginalsBucket  string `yaml:"originalsBucket"`  // 原始简历存储桶
	ParsedTextBucket string `yaml:"parsedTextBucket"` // 解析文本存储桶
	// 新增对象生命周期管理
	OriginalFileExpireDays int  `yaml:"original_file_expire_days"`     // 原始文件过期天数
	ParsedTextExpireDays   int  `yaml:"parsed_text_expire_days"`       // 解析文本过期天数
	EnableTestLogging      bool `yaml:"enable_test_logging,omitempty"` // 新增: 控制测试期间的详细日志记录
}

// MySQLConfig MySQL配置结构
type MySQLConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`
	// 连接池设置
	MaxIdleConns int `yaml:"max_idle_conns"` // 最大空闲连接数
	MaxOpenConns int `yaml:"max_open_conns"` // 最大打开连接数
	// 连接生命周期
	ConnMaxLifetimeMinutes int `yaml:"conn_max_lifetime_minutes"`  // 连接最大生命周期(分钟)
	ConnMaxIdleTimeMinutes int `yaml:"conn_max_idle_time_minutes"` // 空闲连接最大生命周期(分钟)
	// 超时设置
	ConnectTimeoutSeconds int `yaml:"connect_timeout_seconds"` // 连接超时(秒)
	ReadTimeoutSeconds    int `yaml:"read_timeout_seconds"`    // 读取超时(秒)
	WriteTimeoutSeconds   int `yaml:"write_timeout_seconds"`   // 写入超时(秒)
	// 日志设置
	LogLevel int `yaml:"log_level"` // 日志级别(1-4)
}

// ServerConfig 定义服务器配置
type ServerConfig struct {
	Address string `yaml:"address"` // 例如 ":8080" or "0.0.0.0:8080"
}

// LLMParserConfig 定义LLM解析器的配置
type LLMParserConfig struct {
	ModelName         string  `yaml:"modelName"`
	Temperature       float64 `yaml:"temperature"`
	MaxTokens         int     `yaml:"maxTokens"`
	PromptTemplate    string  `yaml:"promptTemplate"`    // 简历分块的提示模板
	ExtractionTimeout string  `yaml:"extractionTimeout"` // 解析超时，例如 "30s"
	QPM               int     `yaml:"qpm"`               // 每分钟请求数限制
	MaxRetries        int     `yaml:"maxRetries"`        // 最大重试次数
	RetryWaitSeconds  int     `yaml:"retryWaitSeconds"`  // 重试等待时间(秒)
}

// JobEvaluatorConfig 定义JD评估器的配置
type JobEvaluatorConfig struct {
	ModelName        string  `yaml:"modelName"`
	Temperature      float64 `yaml:"temperature"`
	MaxTokens        int     `yaml:"maxTokens"`
	PromptTemplate   string  `yaml:"promptTemplate"`   // JD匹配的提示模板
	EvalTimeout      string  `yaml:"evalTimeout"`      // 评估超时
	QPM              int     `yaml:"qpm"`              // 每分钟请求数限制
	MaxRetries       int     `yaml:"maxRetries"`       // 最大重试次数
	RetryWaitSeconds int     `yaml:"retryWaitSeconds"` // 重试等待时间(秒)
}

// QdrantConfig Qdrant向量数据库配置
type QdrantConfig struct {
	Endpoint           string `yaml:"endpoint"`             // Qdrant gRPC 服务地址
	Collection         string `yaml:"collection"`           // 集合名称
	Dimension          int    `yaml:"dimension"`            // 向量维度
	APIKey             string `yaml:"api_key,omitempty"`    // (可选) Qdrant API Key
	DefaultSearchLimit int    `yaml:"default_search_limit"` // 新增：默认搜索结果数量
}

// LoggerConfig 日志配置
type LoggerConfig struct {
	Level        string `yaml:"level"`         // debug, info, warn, error
	Format       string `yaml:"format"`        // json, pretty
	TimeFormat   string `yaml:"time_format"`   // 时间格式
	ReportCaller bool   `yaml:"report_caller"` // 是否报告调用位置
}

// LoadConfig 从文件加载配置
func LoadConfig(configPath string) (*Config, error) {
	// 如果未指定配置文件路径，则尝试在默认位置查找
	if configPath == "" {
		// 尝试在常见位置查找配置文件
		searchPaths := []string{
			"config.yaml",
			"./config.yaml",
			"../config.yaml",
			"../../config.yaml", // 添加更多上级目录
			filepath.Join(os.Getenv("HOME"), ".resume-processor", "config.yaml"),
		}

		// 获取当前可执行文件路径
		execPath, err := os.Executable()
		if err == nil {
			execDir := filepath.Dir(execPath)
			// 添加可执行文件所在目录
			searchPaths = append(searchPaths, filepath.Join(execDir, "config.yaml"))
			// 添加可执行文件上级目录
			searchPaths = append(searchPaths, filepath.Join(execDir, "..", "config.yaml"))
		}

		// 获取工作目录
		workDir, err := os.Getwd()
		if err == nil {
			// 检测是否在测试环境中
			isTest := false
			if strings.Contains(workDir, "tmp") && strings.Contains(workDir, "test") {
				isTest = true
			} else {
				for _, arg := range os.Args {
					if strings.Contains(arg, "test") {
						isTest = true
						break
					}
				}
			}

			// 如果在测试环境中，添加可能的项目根目录
			if isTest {
				// 项目可能的根目录
				projectRoots := []string{
					workDir,
					filepath.Join(workDir, ".."),
					filepath.Join(workDir, "..", ".."),
					filepath.Join(workDir, "..", "..", ".."),
				}
				for _, root := range projectRoots {
					searchPaths = append(searchPaths, filepath.Join(root, "config.yaml"))
				}
			}
		}

		for _, path := range searchPaths {
			if _, err := os.Stat(path); err == nil {
				configPath = path
				break
			}
		}

		// 如果仍找不到配置文件，使用默认路径，但不返回错误
		if configPath == "" {
			// 检测是否在测试环境
			inTest := false
			for _, arg := range os.Args {
				if strings.Contains(arg, "test") {
					inTest = true
					break
				}
			}

			// 如果在测试环境中，创建默认配置
			if inTest {
				// 返回默认配置而不抛出错误
				return createDefaultConfig(), nil
			}

			configPath = "config.yaml"
		}
	}

	// 检查文件是否存在
	_, err := os.Stat(configPath)
	if err != nil {
		// 检测是否在测试环境
		inTest := false
		for _, arg := range os.Args {
			if strings.Contains(arg, "test") {
				inTest = true
				break
			}
		}

		// 如果在测试环境中，返回默认配置而不抛出错误
		if inTest {
			return createDefaultConfig(), nil
		}

		return nil, fmt.Errorf("配置文件不存在: %s", configPath)
	}

	// 读取配置文件
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 解析配置文件
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 从环境变量覆盖配置（如果存在）
	if envKey := os.Getenv("ALIYUN_API_KEY"); envKey != "" {
		config.Aliyun.APIKey = envKey
	}
	if envURL := os.Getenv("ALIYUN_API_URL"); envURL != "" {
		config.Aliyun.APIURL = envURL
	}
	if envModel := os.Getenv("ALIYUN_MODEL"); envModel != "" {
		config.Aliyun.Model = envModel
	}

	// 设置默认值 (如果需要)
	if config.RabbitMQ.RetryInterval == "" {
		config.RabbitMQ.RetryInterval = "5s"
	}
	if config.Server.Address == "" {
		config.Server.Address = ":8080" // 默认服务器地址
	}

	return &config, nil
}

// LoadConfigFromFileOnly 从文件加载配置，不尝试从环境变量覆盖
func LoadConfigFromFileOnly(configPath string) (*Config, error) {
	if configPath == "" {
		return nil, fmt.Errorf("必须提供配置文件路径")
	}

	// 检查文件是否存在
	_, err := os.Stat(configPath)
	if err != nil {
		return nil, fmt.Errorf("配置文件不存在: %s", configPath)
	}

	// 读取配置文件
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	// 解析配置文件
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 注意：此处不从环境变量覆盖Aliyun配置

	// 设置默认值 (如果需要)
	if config.RabbitMQ.RetryInterval == "" {
		config.RabbitMQ.RetryInterval = "5s"
	}
	if config.Server.Address == "" {
		config.Server.Address = ":8080" // 默认服务器地址
	}

	// Ensure embedding defaults are set if not present in YAML
	if config.Aliyun.Embedding.Model == "" {
		config.Aliyun.Embedding.Model = "text-embedding-v3"
	}
	if config.Aliyun.Embedding.Dimensions == 0 {
		config.Aliyun.Embedding.Dimensions = 1024
	}
	if config.Aliyun.Embedding.BaseURL == "" {
		config.Aliyun.Embedding.BaseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/embeddings"
	}

	return &config, nil
}

// 创建一个默认配置，用于测试环境
func createDefaultConfig() *Config {
	config := &Config{}
	// 设置默认值
	config.Aliyun.APIURL = "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions"
	config.Aliyun.Model = "qwen-turbo"
	config.Qdrant.Endpoint = "http://localhost:6333"
	config.Qdrant.Collection = "resumes"
	config.Qdrant.Dimension = 1024

	// Tika默认配置
	config.Tika.ServerURL = "http://localhost:9998"
	config.Tika.Timeout = 60

	// RabbitMQ默认配置
	config.RabbitMQ.URL = "amqp://guest:guest@localhost:5672/"
	config.RabbitMQ.ResumeEventsExchange = "resume.events.exchange"
	config.RabbitMQ.ProcessingEventsExchange = "resume.processing.exchange"
	config.RabbitMQ.RawResumeQueue = "q.raw_resume_uploaded"
	config.RabbitMQ.LLMParsingQueue = "q.resume_for_llm_parsing"
	config.RabbitMQ.UploadedRoutingKey = "resume.uploaded"
	config.RabbitMQ.ParsedRoutingKey = "resume.parsed"
	config.RabbitMQ.MatchEventsExchange = "resume.match.exchange"
	config.RabbitMQ.MatchNeededRoutingKey = "resume.match.needed"
	config.RabbitMQ.JobMatchingQueue = "q.job_matching"
	config.RabbitMQ.PrefetchCount = 10
	config.RabbitMQ.RetryInterval = "5s"
	config.RabbitMQ.MaxRetries = 3
	config.RabbitMQ.ConsumerWorkers = map[string]int{
		"upload_consumer_workers": 5,
		"llm_consumer_workers":    3,
	}
	config.RabbitMQ.BatchTimeouts = map[string]string{
		"upload_batch_timeout": "5s",
	}

	// MinIO默认配置
	config.MinIO.Endpoint = "localhost:9000"
	config.MinIO.AccessKeyID = "minioadmin"
	config.MinIO.SecretAccessKey = "minioadmin123"
	config.MinIO.UseSSL = false
	config.MinIO.BucketName = "originals"
	config.MinIO.Location = ""
	config.MinIO.EnableTestLogging = false // 默认在createDefaultConfig中设置为false

	// MySQL默认配置
	config.MySQL.Host = "localhost"
	config.MySQL.Port = 3306
	config.MySQL.Username = "root"
	config.MySQL.Password = "password"
	config.MySQL.Database = "resume_processor"
	// MySQL连接池默认配置
	config.MySQL.MaxIdleConns = 10
	config.MySQL.MaxOpenConns = 100
	config.MySQL.ConnMaxLifetimeMinutes = 60
	config.MySQL.ConnMaxIdleTimeMinutes = 30
	config.MySQL.ConnectTimeoutSeconds = 10
	config.MySQL.ReadTimeoutSeconds = 30
	config.MySQL.WriteTimeoutSeconds = 30
	config.MySQL.LogLevel = 4 // Info级别

	// Redis默认配置
	config.Redis.Address = "localhost:6379"
	config.Redis.Password = ""
	config.Redis.DB = 0
	// Redis连接池默认配置
	config.Redis.PoolSize = 10
	config.Redis.MinIdleConns = 2
	config.Redis.DialTimeoutSeconds = 5
	config.Redis.ReadTimeoutSeconds = 3
	config.Redis.WriteTimeoutSeconds = 3
	config.Redis.MaxRetries = 3
	config.Redis.MinRetryBackoffMS = 8
	config.Redis.MaxRetryBackoffMS = 512
	config.Redis.ConnMaxLifetimeMinutes = 60
	config.Redis.ConnMaxIdleTimeMinutes = 30
	config.Redis.MD5RecordExpireDays = 365 // 默认1年过期

	// Parser Version 默认配置
	config.ActiveParserVersion = "tika-server-default"

	// 获取环境变量
	if envKey := os.Getenv("ALIYUN_API_KEY"); envKey != "" {
		config.Aliyun.APIKey = envKey
	} else {
		config.Aliyun.APIKey = "test_api_key"
	}

	// 设置默认值 (如果需要)
	if config.RabbitMQ.RetryInterval == "" {
		config.RabbitMQ.RetryInterval = "5s"
	}

	// MinIO对象存储生命周期管理
	config.MinIO.OriginalFileExpireDays = 1095 // 默认3年过期
	config.MinIO.ParsedTextExpireDays = 1095   // 默认3年过期
	config.MinIO.EnableTestLogging = false     // 默认在createDefaultConfig中设置为false (重复设置，保持一致)

	// 日志默认配置
	config.Logger.Level = "info"
	config.Logger.Format = "pretty" // 开发环境默认使用美化输出
	config.Logger.TimeFormat = "2006-01-02 15:04:05"
	config.Logger.ReportCaller = true

	// 添加默认的模型QPM限制
	config.ModelQPMLimits = map[string]int{
		"qwen-max":          1200,
		"qwen-max-latest":   1200,
		"qwen-plus":         15000,
		"qwen-plus-latest":  15000,
		"qwen-turbo":        1200,
		"qwen-turbo-latest": 1200,
	}

	// QdrantConfig 默认配置
	config.Qdrant.APIKey = ""
	config.Qdrant.DefaultSearchLimit = 10

	return config
}

// CreateSampleConfig 创建一个示例配置文件
func CreateSampleConfig(filePath string) error {
	// 检查文件是否已存在
	if _, err := os.Stat(filePath); err == nil {
		return fmt.Errorf("文件 '%s' 已存在，不会覆盖", filePath)
	}

	// 创建一个默认配置实例
	config := createDefaultConfig()

	// 将配置序列化为YAML
	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}

	// 写入文件
	err = os.WriteFile(filePath, data, 0644)
	if err != nil {
		return fmt.Errorf("写入示例配置文件 '%s' 失败: %w", filePath, err)
	}

	fmt.Printf("示例配置文件已创建: %s\n", filePath)
	return nil
}

// GetModelForTask 根据任务名称获取合适的模型
// 如果任务专用模型存在则返回专用模型，否则返回默认模型
func (c *Config) GetModelForTask(taskName string) string {
	// 检查是否有任务专用模型
	if c.Aliyun.TaskModels != nil {
		if model, ok := c.Aliyun.TaskModels[taskName]; ok && model != "" {
			return model
		}
	}
	// 返回默认模型
	return c.Aliyun.Model
}

// GetDuration utility to parse duration strings from config
func GetDuration(durationStr string, defaultDuration time.Duration) time.Duration {
	if durationStr == "" {
		return defaultDuration
	}
	d, err := time.ParseDuration(durationStr)
	if err != nil {
		return defaultDuration
	}
	return d
}
