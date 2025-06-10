package main

import (
	"ai-agent-go/internal/config"
	"ai-agent-go/internal/parser"
	"ai-agent-go/internal/storage"
	"ai-agent-go/internal/types"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// 配置并发数
const (
	concurrency        = 5
	batchSize          = 20
	embeddingBatchSize = 20
)

func main() {
	// 设置日志输出
	logFile, err := os.Create("data_repair.log")
	if err != nil {
		log.Fatalf("创建日志文件失败: %v", err)
	}
	defer logFile.Close()
	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// 创建上下文
	ctx := context.Background()

	// 加载配置
	cfg, err := config.LoadConfig("")
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	// 初始化存储
	storageManager, err := storage.NewStorage(ctx, cfg)
	if err != nil {
		log.Fatalf("初始化存储失败: %v", err)
	}
	defer storageManager.Close()

	// 初始化嵌入器
	aliyunEmbeddingConfig := config.EmbeddingConfig{
		Model:      cfg.Aliyun.Embedding.Model,
		BaseURL:    cfg.Aliyun.Embedding.BaseURL,
		Dimensions: cfg.Aliyun.Embedding.Dimensions,
	}
	embedder, err := parser.NewAliyunEmbedder(cfg.Aliyun.APIKey, aliyunEmbeddingConfig)
	if err != nil {
		log.Fatalf("初始化embedder失败: %v", err)
	}

	// 获取所有需要处理的简历UUID
	submissions, err := getSubmissionsToProcess(ctx, storageManager)
	if err != nil {
		log.Fatalf("获取待处理简历列表失败: %v", err)
	}
	log.Printf("总共找到 %d 个简历需要处理", len(submissions))

	// 使用信号量控制并发
	semaphore := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	// 分批处理submissions
	for i := 0; i < len(submissions); i += batchSize {
		end := i + batchSize
		if end > len(submissions) {
			end = len(submissions)
		}

		currentBatch := submissions[i:end]
		log.Printf("处理批次 %d-%d, 共 %d 个简历", i, end-1, len(currentBatch))

		for _, submissionUUID := range currentBatch {
			wg.Add(1)
			semaphore <- struct{}{}

			go func(uuid string) {
				defer func() {
					<-semaphore
					wg.Done()
				}()

				if err := processSubmission(ctx, storageManager, embedder, uuid); err != nil {
					log.Printf("处理简历 %s 失败: %v", uuid, err)
				} else {
					log.Printf("✅ 简历 %s 处理完成", uuid)
				}
			}(submissionUUID)
		}

		// 等待当前批次完成
		wg.Wait()
		log.Printf("批次 %d-%d 处理完成，休息5秒...", i, end-1)
		time.Sleep(5 * time.Second)
	}

	log.Println("所有简历处理完成")
}

// 获取需要处理的简历UUID列表
func getSubmissionsToProcess(ctx context.Context, storageManager *storage.Storage) ([]string, error) {
	var submissions []string

	err := storageManager.MySQL.DB().WithContext(ctx).Raw(`
		SELECT DISTINCT rsc.submission_uuid 
		FROM resume_submission_chunks rsc
		JOIN resume_submissions rs ON rs.submission_uuid = rsc.submission_uuid
		WHERE (rs.qdrant_point_ids IS NULL OR rs.qdrant_point_ids = '[]' OR rs.qdrant_point_ids = '')
		AND rsc.point_id IS NULL OR rsc.point_id LIKE 'point-%'
	`).Scan(&submissions).Error

	if err != nil {
		return nil, fmt.Errorf("查询需要处理的简历失败: %w", err)
	}

	return submissions, nil
}

// 处理单个简历
func processSubmission(ctx context.Context, storageManager *storage.Storage, embedder *parser.AliyunEmbedder, submissionUUID string) error {
	log.Printf("开始处理简历 %s", submissionUUID)

	// 1. 获取简历基本信息
	basicInfo, err := getBasicInfoForSubmission(ctx, storageManager, submissionUUID)
	if err != nil {
		return fmt.Errorf("获取基本信息失败: %w", err)
	}

	// 2. 获取简历分块
	chunks, err := getChunksForSubmission(ctx, storageManager, submissionUUID)
	if err != nil {
		return fmt.Errorf("获取简历分块失败: %w", err)
	}
	log.Printf("简历 %s 有 %d 个分块需处理", submissionUUID, len(chunks))

	// 3. 预先为每个分块生成新的point_id
	for i := range chunks {
		chunks[i].PointID = uuid.New().String()
	}

	// 4. 准备向量化数据
	resumeChunks, texts := prepareChunksForEmbedding(chunks, basicInfo)

	// 5. 分批执行嵌入以避免超出API限制
	var allEmbeddings [][]float64

	for i := 0; i < len(texts); i += embeddingBatchSize {
		end := i + embeddingBatchSize
		if end > len(texts) {
			end = len(texts)
		}

		batchTexts := texts[i:end]
		log.Printf("为简历 %s 执行嵌入批次 %d-%d", submissionUUID, i, end-1)

		embeddings, err := embedder.EmbedStrings(ctx, batchTexts)
		if err != nil {
			return fmt.Errorf("生成嵌入向量失败: %w", err)
		}

		allEmbeddings = append(allEmbeddings, embeddings...)

		// 避免过快请求API
		if end < len(texts) {
			time.Sleep(1 * time.Second)
		}
	}

	// 6. 存储到Qdrant
	pointIDs, err := storageManager.Qdrant.StoreResumeVectors(ctx, submissionUUID, resumeChunks, allEmbeddings)
	if err != nil {
		return fmt.Errorf("存储向量到Qdrant失败: %w", err)
	}
	log.Printf("简历 %s 的向量已存入Qdrant，获得 %d 个point_id", submissionUUID, len(pointIDs))

	// 7. 更新MySQL中的point_id和resume_submissions表
	err = updateDatabase(ctx, storageManager, chunks, pointIDs, submissionUUID)
	if err != nil {
		return fmt.Errorf("更新数据库失败: %w", err)
	}

	return nil
}

// 查询简历chunk
type ChunkData struct {
	ChunkDBID           uint64
	SubmissionUUID      string
	ChunkIDInSubmission int
	ChunkType           string
	ChunkTitle          string
	ChunkContentText    string
	PointID             string
}

// 获取指定简历的所有分块
func getChunksForSubmission(ctx context.Context, storageManager *storage.Storage, submissionUUID string) ([]ChunkData, error) {
	var chunks []ChunkData

	err := storageManager.MySQL.DB().WithContext(ctx).Raw(`
		SELECT 
			chunk_db_id, submission_uuid, chunk_id_in_submission, 
			chunk_type, IFNULL(chunk_title, ''), chunk_content_text, 
			IFNULL(point_id, '')
		FROM resume_submission_chunks
		WHERE submission_uuid = ?
		ORDER BY chunk_id_in_submission
	`, submissionUUID).Scan(&chunks).Error

	if err != nil {
		return nil, err
	}

	return chunks, nil
}

// 获取简历基本信息
func getBasicInfoForSubmission(ctx context.Context, storageManager *storage.Storage, submissionUUID string) (map[string]string, error) {
	var llmParsedBasicInfoJSON sql.NullString

	err := storageManager.MySQL.DB().WithContext(ctx).Raw(`
		SELECT IFNULL(llm_parsed_basic_info, '{}') 
		FROM resume_submissions 
		WHERE submission_uuid = ?
	`, submissionUUID).Scan(&llmParsedBasicInfoJSON).Error

	if err != nil {
		return nil, err
	}

	basicInfo := make(map[string]string)

	// 解析JSON到map
	if llmParsedBasicInfoJSON.Valid && llmParsedBasicInfoJSON.String != "" && llmParsedBasicInfoJSON.String != "{}" {
		var jsonMap map[string]interface{}
		if err := json.Unmarshal([]byte(llmParsedBasicInfoJSON.String), &jsonMap); err != nil {
			log.Printf("警告: 解析llm_parsed_basic_info JSON失败: %v", err)
		} else {
			// 将interface{}值转换为string
			for k, v := range jsonMap {
				switch val := v.(type) {
				case string:
					basicInfo[k] = val
				case float64:
					basicInfo[k] = fmt.Sprintf("%.1f", val)
				case int:
					basicInfo[k] = strconv.Itoa(val)
				case bool:
					if val {
						basicInfo[k] = "true"
					} else {
						basicInfo[k] = "false"
					}
				default:
					// 尝试JSON编组
					if b, err := json.Marshal(v); err == nil {
						basicInfo[k] = string(b)
					}
				}
			}
		}
	}

	// 检查是否缺少关键字段，可以从chunk内容中提取
	if _, ok := basicInfo["name"]; !ok {
		basicInfo["name"] = "未知"
	}
	if _, ok := basicInfo["phone"]; !ok {
		basicInfo["phone"] = "未知"
	}
	if _, ok := basicInfo["email"]; !ok {
		basicInfo["email"] = "未知"
	}

	return basicInfo, nil
}

// 准备向量化数据
func prepareChunksForEmbedding(chunks []ChunkData, basicInfo map[string]string) ([]types.ResumeChunk, []string) {
	resumeChunks := make([]types.ResumeChunk, len(chunks))
	texts := make([]string, len(chunks))

	for i, chunk := range chunks {
		// 1. 准备唯一标识符
		identifiers := types.Identity{
			Name:  basicInfo["name"],
			Phone: basicInfo["phone"],
			Email: basicInfo["email"],
		}

		// 2. 提取元数据
		experienceYears := 0
		if yearsStr, ok := basicInfo["years_of_experience"]; ok {
			if years, err := strconv.ParseFloat(yearsStr, 64); err == nil {
				experienceYears = int(years)
			}
		}

		metadata := types.ChunkMetadata{
			ExperienceYears: experienceYears,
			EducationLevel:  basicInfo["highest_education"],
		}

		// 3. 确定重要性分数
		importanceScore := getImportanceScoreByType(chunk.ChunkType)

		// 4. 创建ResumeChunk
		resumeChunks[i] = types.ResumeChunk{
			ChunkID:           chunk.ChunkIDInSubmission,
			ChunkType:         chunk.ChunkType,
			Content:           chunk.ChunkContentText,
			ImportanceScore:   importanceScore,
			UniqueIdentifiers: identifiers,
			Metadata:          metadata,
		}

		// 5. 准备嵌入文本 - 不同类型有不同的处理方式
		var embeddingText string
		if chunk.ChunkType == "BASIC_INFO" {
			embeddingText = fmt.Sprintf("简历基本信息: %s", chunk.ChunkContentText)
		} else if chunk.ChunkTitle != "" {
			embeddingText = fmt.Sprintf("%s: %s", chunk.ChunkTitle, chunk.ChunkContentText)
		} else {
			embeddingText = chunk.ChunkContentText
		}
		texts[i] = embeddingText
	}

	return resumeChunks, texts
}

// 根据分块类型确定重要性分数
func getImportanceScoreByType(chunkType string) float32 {
	switch chunkType {
	case "BASIC_INFO":
		return 1.0
	case "WORK_EXPERIENCE":
		return 0.95
	case "PROJECTS":
		return 0.9
	case "INTERNSHIPS":
		return 0.85
	case "SKILLS":
		return 0.8
	case "EDUCATION":
		return 0.75
	case "AWARDS":
		return 0.7
	case "PORTFOLIO":
		return 0.65
	default:
		return 0.5
	}
}

// 更新数据库中的point_id和qdrant_point_ids
func updateDatabase(ctx context.Context, storageManager *storage.Storage, chunks []ChunkData, pointIDs []string, submissionUUID string) error {
	// 使用GORM事务
	return storageManager.MySQL.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 更新resume_submission_chunks表
		for i, chunk := range chunks {
			if i < len(pointIDs) {
				if err := tx.Exec("UPDATE resume_submission_chunks SET point_id = ? WHERE chunk_db_id = ?",
					pointIDs[i], chunk.ChunkDBID).Error; err != nil {
					return err
				}
			} else {
				log.Printf("警告: Qdrant返回的pointIDs少于chunks, 使用预先生成的ID")
				if err := tx.Exec("UPDATE resume_submission_chunks SET point_id = ? WHERE chunk_db_id = ?",
					chunk.PointID, chunk.ChunkDBID).Error; err != nil {
					return err
				}
			}
		}

		// 更新resume_submissions表的qdrant_point_ids字段
		pointIDsJSON, err := json.Marshal(pointIDs)
		if err != nil {
			return err
		}

		if err := tx.Exec(`
			UPDATE resume_submissions 
			SET qdrant_point_ids = ? 
			WHERE submission_uuid = ?
		`, string(pointIDsJSON), submissionUUID).Error; err != nil {
			return err
		}

		return nil
	})
}
