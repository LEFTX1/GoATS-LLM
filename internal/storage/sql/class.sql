-- 1 candidates (候选人主表)
CREATE TABLE candidates (
                            candidate_id CHAR(36) PRIMARY KEY,         -- 候选人的全局唯一ID (UUIDv7字符串形式)
                            primary_name VARCHAR(255),                  -- 候选人的主要姓名
                            primary_phone VARCHAR(50),                     -- 候选人的主要手机号 (清洗和标准化后)
                            primary_email VARCHAR(255),                    -- 候选人的主要邮箱 (清洗和标准化后)
                            gender VARCHAR(10),                           -- 性别 (如: MALE, FEMALE, OTHER, UNKNOWN)
                            birth_date DATE,                              -- 出生日期
                            current_location VARCHAR(255),                -- 当前所在地
                            profile_summary TEXT,                         -- 候选人整体简介/亮点
                            created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
                            updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 唯一约束 (MySQL中NULL值在UNIQUE索引中被视为不同，所以可以直接创建)
ALTER TABLE candidates ADD UNIQUE INDEX idx_candidates_primary_phone_unique (primary_phone);
ALTER TABLE candidates ADD UNIQUE INDEX idx_candidates_primary_email_unique (primary_email);

-- 注释 (MySQL中表和列的注释方式)
ALTER TABLE candidates COMMENT = '存储独立候选人的核心档案信息，经过身份识别和去重处理。';
ALTER TABLE candidates MODIFY candidate_id CHAR(36) COMMENT '候选人的全局唯一ID (UUIDv7字符串形式)。';


-- 4 jobs (岗位信息表)
CREATE TABLE jobs (
                      job_id CHAR(36) PRIMARY KEY,                   -- 岗位唯一ID (UUIDv7)
                      job_title VARCHAR(255) NOT NULL,               -- 岗位名称
                      department VARCHAR(255),                       -- 所属部门
                      location VARCHAR(255),                         -- 工作地点
                      job_description_text TEXT NOT NULL,            -- 完整的岗位描述原文
                      structured_requirements_json JSON,             -- LLM或规则提取的JD结构化要求
                      jd_skills_keywords_json JSON,                  -- 从JD中提取的核心技能关键词列表
                      status VARCHAR(50) DEFAULT 'ACTIVE',           -- 岗位状态 (ACTIVE, INACTIVE, CLOSED, DRAFT)
                      created_by_user_id CHAR(36),                   -- 创建该岗位的HR用户ID (可能是系统用户或其他表的外键)
                      created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
                      updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
                      INDEX idx_jobs_status (status)
    -- FULLTEXT INDEX idx_jobs_job_title_ft (job_title) -- MySQL的全文索引
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE jobs COMMENT = '存储招聘岗位信息。';

-- 2 resume_submissions (简历提交/快照表)
CREATE TABLE resume_submissions (
                                    submission_uuid CHAR(36) PRIMARY KEY,       -- 本次简历处理实例的全局唯一ID (UUIDv7)
                                    candidate_id CHAR(36),                      -- 关联的候选人ID (外键, UUIDv7)
                                    submission_timestamp DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6), -- 简历提交或系统接收时间
                                    source_channel VARCHAR(100),               -- 简历来源渠道
                                    target_job_id CHAR(36),                    -- 本次投递的目标岗位ID (外键, UUIDv7)
                                    original_filename VARCHAR(255),              -- 原始简历文件名
                                    original_file_path_oss VARCHAR(1024),      -- 原始简历文件在对象存储中的路径
                                    parsed_text_path_oss VARCHAR(1024),        -- 解析后的纯文本在对象存储中的路径
                                    raw_text_md5 CHAR(32),                       -- 纯文本内容的MD5
                                    llm_parsed_basic_info JSON,                -- LLM解析出的BasicInfo (含原metadata) 的JSON对象
                                    llm_resume_identifier VARCHAR(255),        -- LLM从简历中提取的“姓名_电话号码”标识
                                    processing_status VARCHAR(50) DEFAULT 'PENDING_PARSING', -- 处理状态
                                    parser_version VARCHAR(50),                  -- 使用的简历解析器版本
                                    created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
                                    updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
                                    INDEX idx_rs_candidate_id (candidate_id),
                                    INDEX idx_rs_target_job_id (target_job_id),
                                    INDEX idx_rs_submission_timestamp (submission_timestamp),
                                    INDEX idx_rs_processing_status (processing_status),
                                    INDEX idx_rs_raw_text_md5 (raw_text_md5),
                                    CONSTRAINT fk_rs_candidate_id FOREIGN KEY (candidate_id) REFERENCES candidates(candidate_id) ON DELETE SET NULL,
                                    CONSTRAINT fk_rs_target_job_id FOREIGN KEY (target_job_id) REFERENCES jobs(job_id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE resume_submissions COMMENT = '存储每一次简历的提交记录或系统处理快照。';

-- 3 resume_submission_chunks (简历分块文本表)
CREATE TABLE resume_submission_chunks (
                                          chunk_db_id BIGINT AUTO_INCREMENT PRIMARY KEY, -- 数据库自增主键
                                          submission_uuid CHAR(36) NOT NULL,             -- 关联的简历提交实例ID (外键, UUIDv7)
                                          chunk_id_in_submission INT NOT NULL,          -- 该块在本次提交简历中的原始chunk_id (来自LLM)
                                          chunk_type VARCHAR(50) NOT NULL,              -- 块类型 (SKILLS, PROJECTS, EDUCATION, etc.)
                                          chunk_title TEXT,                            -- 块的原始标题 (可能为空)
                                          chunk_content_text TEXT NOT NULL,            -- 块的完整文本内容
                                          created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
                                          INDEX idx_rsc_submission_uuid (submission_uuid),
                                          INDEX idx_rsc_chunk_type (chunk_type),
                                          UNIQUE INDEX idx_rsc_submission_chunk_id (submission_uuid, chunk_id_in_submission),
                                          CONSTRAINT fk_rsc_submission_uuid FOREIGN KEY (submission_uuid) REFERENCES resume_submissions(submission_uuid) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE resume_submission_chunks COMMENT = '存储LLM从每次简历提交中解析出的文本块。';


-- 5 job_submission_matches (岗位-简历投递匹配评估表)
CREATE TABLE job_submission_matches (
                                        match_id BIGINT AUTO_INCREMENT PRIMARY KEY,      -- 匹配记录的自增主键
                                        submission_uuid CHAR(36) NOT NULL,               -- 关联的简历提交实例ID (外键, UUIDv7)
                                        job_id CHAR(36) NOT NULL,                        -- 关联的岗位ID (外键, UUIDv7)
                                        llm_match_score INT,                             -- LLM给出的匹配度评分 (0-100)
                                        llm_match_highlights_json JSON,                  -- LLM生成的匹配亮点 (JSON数组)
                                        llm_potential_gaps_json JSON,                    -- LLM生成的潜在不足 (JSON数组)
                                        llm_resume_summary_for_jd TEXT,                  -- LLM针对此JD生成的简历摘要
                                        vector_match_score FLOAT,                        -- (可选) 基于向量相似度计算的匹配分
                                        vector_score_details_json JSON,                  -- (可选) 向量匹配的详细分项得分或证据
                                        overall_calculated_score FLOAT,                  -- 系统综合计算的总匹配分 (用于排序)
                                        evaluation_status VARCHAR(50) DEFAULT 'PENDING', -- 评估状态
                                        evaluated_at DATETIME(6),                        -- 评估完成时间
                                        hr_feedback_status VARCHAR(50),                  -- HR的反馈状态
                                        hr_feedback_notes TEXT,                          -- HR的备注
                                        created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
                                        updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
                                        INDEX idx_jsm_job_id_overall_score (job_id, overall_calculated_score DESC),
                                        INDEX idx_jsm_submission_uuid (submission_uuid),
                                        INDEX idx_jsm_evaluation_status (evaluation_status),
                                        INDEX idx_jsm_hr_feedback_status (hr_feedback_status),
                                        UNIQUE INDEX idx_jsm_submission_job_unique (submission_uuid, job_id),
                                        CONSTRAINT fk_jsm_submission_uuid FOREIGN KEY (submission_uuid) REFERENCES resume_submissions(submission_uuid) ON DELETE CASCADE,
                                        CONSTRAINT fk_jsm_job_id FOREIGN KEY (job_id) REFERENCES jobs(job_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE job_submission_matches COMMENT = '存储特定简历提交与特定岗位的LLM及向量匹配评估结果。';

-- 7 interviewers (面试官信息表)
CREATE TABLE interviewers (
                              interviewer_id CHAR(36) PRIMARY KEY,       -- 面试官的唯一ID (建议UUIDv7)
                              user_id CHAR(36) UNIQUE,                   -- (可选) 如果面试官关联到系统用户表的user_id
                              name VARCHAR(255) NOT NULL,                -- 面试官姓名
                              email VARCHAR(255) UNIQUE NOT NULL,        -- 面试官工作邮箱
                              department VARCHAR(255),                   -- 所属部门
                              title VARCHAR(255),                        -- 职位/职级
    -- 可以添加其他面试官相关信息，如擅长领域、面试经验等
                              is_active BOOLEAN DEFAULT TRUE,            -- 是否仍然是活跃面试官
                              created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
                              updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
    -- 如果user_id存在，可以添加外键:
    -- CONSTRAINT fk_interviewer_user_id FOREIGN KEY (user_id) REFERENCES users(user_id) ON DELETE SET NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

ALTER TABLE interviewers COMMENT = '存储面试官的基本信息。';
ALTER TABLE interviewers MODIFY interviewer_id CHAR(36) COMMENT '面试官的唯一ID (UUIDv7字符串形式)。';
ALTER TABLE interviewers MODIFY user_id CHAR(36) UNIQUE COMMENT '(可选) 关联的系统用户ID，如果面试官也是系统用户。';
ALTER TABLE interviewers MODIFY email VARCHAR(255) UNIQUE NOT NULL COMMENT '面试官工作邮箱，用于联系和登录（如果需要）。';


-- 6 interview_evaluations (面试评价表) - 新增
CREATE TABLE interview_evaluations (
                                       evaluation_id BIGINT AUTO_INCREMENT PRIMARY KEY, -- 面评记录的自增主键
                                       candidate_id CHAR(36) NOT NULL,                   -- 关联的候选人ID (外键, UUIDv7)
                                       job_id CHAR(36) NOT NULL,                        -- 本次面试关联的岗位ID (外键, UUIDv7)
                                       submission_uuid CHAR(36),                        -- (可选) 本次面试基于的简历提交ID (外键, UUIDv7)
                                       interviewer_id CHAR(36) NOT NULL,                -- 面试官的用户ID (外键, 指向interviewers表)
                                       interview_round VARCHAR(100) NOT NULL,           -- 面试轮次 (如: '一面', '二面', '技术面', 'HR面')
                                       interview_date DATETIME(6) NOT NULL,             -- 面试日期和时间
                                       overall_rating INT,                              -- 综合评分 (例如 1-5 或 1-10)
                                       evaluation_summary TEXT,                         -- 面评总结/核心结论
                                       strengths TEXT,                                  -- 候选人优点
                                       weaknesses TEXT,                                 -- 候选人待提高点/不足
                                       specific_skill_ratings JSON,                     -- (可选) 对特定技能的评分 (如: {"Java": 4, "沟通能力": 5})
                                       hiring_recommendation VARCHAR(50),               -- 录用建议 (如: '强烈推荐', '推荐', '待定', '不推荐')
                                       notes TEXT,                                      -- 其他备注
                                       created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
                                       updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
                                       FOREIGN KEY (candidate_id) REFERENCES candidates(candidate_id) ON DELETE CASCADE,
                                       FOREIGN KEY (job_id) REFERENCES jobs(job_id) ON DELETE CASCADE,
                                       FOREIGN KEY (submission_uuid) REFERENCES resume_submissions(submission_uuid) ON DELETE SET NULL,
                                       FOREIGN KEY (interviewer_id) REFERENCES interviewers(interviewer_id) ON DELETE RESTRICT -- 或者 ON DELETE SET NULL，取决于业务逻辑
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- 原有的索引保持不变或根据查询需求调整
CREATE INDEX idx_ie_candidate_id ON interview_evaluations(candidate_id);
CREATE INDEX idx_ie_job_id ON interview_evaluations(job_id);
CREATE INDEX idx_ie_interviewer_id ON interview_evaluations(interviewer_id);
CREATE INDEX idx_ie_interview_date ON interview_evaluations(interview_date);

ALTER TABLE interview_evaluations COMMENT = '存储面试官对候选人在特定岗位特定轮次的面试评价。';
ALTER TABLE interview_evaluations MODIFY interviewer_id CHAR(36) NOT NULL COMMENT '面试官的ID，关联到interviewers表。';
