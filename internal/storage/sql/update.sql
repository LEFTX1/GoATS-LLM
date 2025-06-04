-- 增量 SQL 脚本 --

-- 1. 创建 job_vectors 表
CREATE TABLE job_vectors (
    job_id CHAR(36) PRIMARY KEY,
    vector_representation MEDIUMBLOB NOT NULL COMMENT '存储二进制向量表示，例如从 embedding 模型得到的 float32 数组序列化后的结果',
    embedding_model_version VARCHAR(100) NOT NULL COMMENT '生成此向量所使用的 embedding 模型的版本或名称',
    created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    CONSTRAINT fk_job_vectors_job_id FOREIGN KEY (job_id) REFERENCES jobs(job_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT '存储岗位的向量表示及其模型版本。';

-- 2. 修改 resume_submissions 表：添加 similarity_hash 字段和唯一索引
ALTER TABLE resume_submissions
    ADD COLUMN similarity_hash CHAR(16) NULL COMMENT '简历纯文本内容的SimHash值 (例如64位SimHash的16进制表示)，用于近重复检测' AFTER raw_text_md5,
    ADD UNIQUE INDEX idx_rs_similarity_hash (similarity_hash);

-- 更新 resume_submissions 表中现有字段的注释 (可选，如果需要同步)
ALTER TABLE resume_submissions MODIFY raw_text_md5 CHAR(32) COMMENT '简历纯文本内容的MD5 (用于精确去重)。';

-- 3. 创建 reviewed_resumes 表
CREATE TABLE reviewed_resumes (
    review_id BIGINT AUTO_INCREMENT PRIMARY KEY COMMENT '审阅记录的自增主键',
    job_id CHAR(36) NOT NULL COMMENT '关联的岗位ID',
    hr_id CHAR(36) NOT NULL COMMENT '执行审阅的HR用户ID',
    text_md5 CHAR(32) NOT NULL COMMENT '被审阅简历内容的MD5',
    submission_uuid CHAR(36) NULL COMMENT '本次审阅时参考的具体简历提交快照ID (可选,仅供追溯)',
    action VARCHAR(50) NOT NULL COMMENT '审阅动作 (例如: REJECTED, APPROVED, SHORTLISTED, CONTACTED)',
    reason_text TEXT NULL COMMENT '审阅理由或备注',
    idempotency_key CHAR(36) NULL COMMENT '用于保证POST请求幂等性客户端提供的键',
    version INT DEFAULT 1 COMMENT '记录版本号，用于乐观锁控制并发更新',
    created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
    updated_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
    UNIQUE KEY uq_reviewed_job_hr_text_md5 (job_id, hr_id, text_md5) COMMENT '确保同一HR对同一岗位下的同一份简历内容只有一条审阅记录',
    UNIQUE KEY uq_reviewed_idempotency_key (idempotency_key) COMMENT '确保幂等键的唯一性',
    INDEX idx_reviewed_job_text_md5 (job_id, text_md5) COMMENT '方便查询特定岗位下某份简历的所有HR审阅情况',
    CONSTRAINT fk_reviewed_resumes_job_id FOREIGN KEY (job_id) REFERENCES jobs(job_id) ON DELETE CASCADE,
    CONSTRAINT fk_reviewed_resumes_submission_uuid FOREIGN KEY (submission_uuid) REFERENCES resume_submissions(submission_uuid) ON DELETE SET NULL
    -- 假设存在一个hr_users表:
    -- CONSTRAINT fk_reviewed_resumes_hr_id FOREIGN KEY (hr_id) REFERENCES hr_users(hr_id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT '存储HR对特定简历内容在特定岗位下的审阅记录及反馈。';
