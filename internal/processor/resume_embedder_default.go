package processor // 定义了简历处理相关的核心逻辑和组件

import ( // 导入所需的包
	"ai-agent-go/internal/types" // 导入项目自定义的类型
	"context"                    // 导入上下文包
	"fmt"                        // 导入格式化I/O包
)

// DefaultResumeEmbedder 是 ResumeEmbedder 接口的默认实现。
type DefaultResumeEmbedder struct { // 定义 DefaultResumeEmbedder 结构体
	textEmbedder TextEmbedder // 包含一个 TextEmbedder 接口类型的字段，用于文本嵌入
}

// NewDefaultResumeEmbedder 创建一个新的 DefaultResumeEmbedder 实例。
func NewDefaultResumeEmbedder(textEmbedder TextEmbedder) (*DefaultResumeEmbedder, error) { // 函数接收 TextEmbedder 参数，返回 DefaultResumeEmbedder 指针和错误
	if textEmbedder == nil { // 检查传入的 textEmbedder 是否为 nil
		return nil, fmt.Errorf("textEmbedder cannot be nil") // 如果为 nil，则返回错误信息
	}
	return &DefaultResumeEmbedder{textEmbedder: textEmbedder}, nil // 返回新创建的 DefaultResumeEmbedder 实例和 nil 错误
}

// EmbedResumeChunks 实现了 ResumeEmbedder 接口。
// 它将简历的各个部分和基本信息转换为文本，然后进行嵌入。
func (dre *DefaultResumeEmbedder) EmbedResumeChunks(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) { // 方法接收上下文、简历分块和基本信息，返回简历块向量切片和错误
	if dre.textEmbedder == nil { // 检查 DefaultResumeEmbedder 中的 textEmbedder 是否未初始化
		return nil, fmt.Errorf("textEmbedder is not initialized in DefaultResumeEmbedder") // 如果未初始化，则返回错误信息
	}

	var textsToEmbed []string                        // 定义一个字符串切片，用于存储待嵌入的文本
	var correspondingSections []*types.ResumeSection // 定义一个 ResumeSection 指针切片，用于存储与待嵌入文本对应的原始简历部分
	// var isBasicInfoChunk []bool // 不再需要标记basicInfo块，因为我们不再单独嵌入它

	// Process resume sections
	// 处理简历的各个部分
	for _, section := range sections { // 遍历传入的 sections 切片
		if section.Content == "" { // 检查当前 section 的内容是否为空
			continue // 如果内容为空，则跳过当前循环
		}
		var textRepresentation string // 定义一个字符串变量，用于存储当前 section 的文本表示
		if section.Title != "" {      // 检查当前 section 的标题是否不为空
			// 为了更纯粹的内容嵌入，这里可以考虑是否包含Title，或者仅嵌入Content
			// 当前保留Title以提供上下文
			textRepresentation = fmt.Sprintf("Section - %s: %s", section.Title, section.Content) // 如果标题不为空，则将标题和内容格式化为 "Section - [标题]: [内容]" 的形式
		} else { // 如果标题为空
			textRepresentation = section.Content // 则直接使用 section 的内容作为文本表示
		}
		textsToEmbed = append(textsToEmbed, textRepresentation)        // 将生成的文本表示添加到 textsToEmbed 中
		correspondingSections = append(correspondingSections, section) // 将当前 section 添加到 correspondingSections 中
		// isBasicInfoChunk = append(isBasicInfoChunk, false) // 不再需要
	}

	if len(textsToEmbed) == 0 { // 检查 textsToEmbed 是否为空
		return []*types.ResumeChunkVector{}, nil // 如果为空，则返回一个空的 ResumeChunkVector 切片和 nil 错误
	}

	embeddings, err := dre.textEmbedder.EmbedStrings(ctx, textsToEmbed) // 调用 textEmbedder 的 EmbedStrings 方法对 textsToEmbed 中的所有文本进行嵌入
	if err != nil {                                                     // 检查嵌入过程中是否发生错误
		return nil, fmt.Errorf("embedding texts failed: %w", err) // 如果发生错误，则返回包装后的错误信息
	}

	if len(embeddings) != len(textsToEmbed) { // 检查返回的嵌入向量数量是否与待嵌入文本数量一致
		return nil, fmt.Errorf("embedding count mismatch: expected %d, got %d", len(textsToEmbed), len(embeddings)) // 如果数量不一致，则返回错误信息
	}

	var resumeChunkVectors []*types.ResumeChunkVector // 定义一个 ResumeChunkVector 指针切片，用于存储最终生成的简历块向量
	for i, embeddingVector := range embeddings {      // 遍历生成的嵌入向量
		if len(embeddingVector) == 0 { // 检查当前嵌入向量是否为空
			continue // 如果为空，则跳过当前循环
		}

		originalSection := correspondingSections[i] // 获取与当前嵌入向量对应的原始简历部分
		metadata := make(map[string]interface{})    // 创建一个 map 用于存储元数据

		metadata["original_chunk_type"] = string(originalSection.Type) // 将原始分块类型添加到元数据中
		if originalSection.Title != "" {                               // 检查原始分块的标题是否不为空
			metadata["original_chunk_title"] = originalSection.Title // 如果不为空，则将原始分块标题添加到元数据中
		}
		if originalSection.ChunkID > 0 { // 检查原始分块的 ChunkID 是否大于0
			metadata["llm_chunk_id"] = originalSection.ChunkID // 如果大于0，则将 llm_chunk_id 添加到元数据中
		}
		if originalSection.ResumeIdentifier != "" { // 检查原始分块的简历标识符是否不为空
			// ResumeIdentifier 通常从 basicInfo 中来，这里确保它被正确关联
			metadata["llm_resume_identifier"] = originalSection.ResumeIdentifier
		} else if biRI, ok := basicInfo["resume_identifier"]; ok && biRI != "" {
			// 如果 section 本身没有，尝试从 basicInfo 获取
			metadata["llm_resume_identifier"] = biRI
		}

		// 将 basicInfo 中的相关信息添加到每个 chunk 的 metadata 中
		// 这是因为 basicInfo 现在不单独生成向量，但其信息对每个 chunk 都可能有用
		if basicInfo != nil { // 如果基本信息不为空
			for k, v := range basicInfo { // 遍历基本信息
				// 避免覆盖上面已经从 originalSection 设置的字段，除非特定需要
				if _, exists := metadata[k]; !exists { // 如果元数据中不存在该键
					metadata[k] = v // 则添加
				}
			}
		}
		// 确保一些关键的 basicInfo 字段（如果存在）被包含
		// 例如，如果 "name", "phone", "email" 等字段在 basicInfo 中，它们会在这里被加入 metadata
		// 也可以选择性地只添加 "valuableBasicKeys" 对应的字段到metadata

		resumeChunkVectors = append(resumeChunkVectors, &types.ResumeChunkVector{ // 创建一个新的 ResumeChunkVector 并将其指针添加到 resumeChunkVectors 中
			Section:  originalSection, // 设置原始简历部分
			Vector:   embeddingVector, // 设置嵌入向量
			Metadata: metadata,        // 设置元数据
		})
	}

	return resumeChunkVectors, nil // 返回生成的简历块向量切片和 nil 错误
}

// Embed 是 EmbedResumeChunks 的别名，用于满足 ResumeEmbedder 接口。
func (dre *DefaultResumeEmbedder) Embed(ctx context.Context, sections []*types.ResumeSection, basicInfo map[string]string) ([]*types.ResumeChunkVector, error) { // 定义 Embed 方法，参数和返回值与 EmbedResumeChunks 一致
	return dre.EmbedResumeChunks(ctx, sections, basicInfo) // 调用 EmbedResumeChunks 方法并返回其结果
}
