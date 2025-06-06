-- 增量 SQL 脚本 for performance improvements --
-- Based on analysis from 2024-07-29

-- 1. 为 jobs.job_description_text 添加全文索引以支持关键字搜索
ALTER TABLE jobs ADD FULLTEXT INDEX idx_jobs_description_ft (job_description_text);

-- 2. 为 interviewers.is_active 添加索引以快速筛选活跃面试官
ALTER TABLE interviewers ADD INDEX idx_interviewers_is_active (is_active);

-- 3. 为 interview_evaluations 添加全文索引以支持在面评中进行文本搜索
ALTER TABLE interview_evaluations ADD FULLTEXT INDEX idx_ie_feedback_ft (evaluation_summary, strengths, weaknesses);

-- 4. 为 interview_evaluations 添加复合索引以快速查询特定岗位的推荐候选人
ALTER TABLE interview_evaluations ADD INDEX idx_ie_job_recommendation (job_id, hiring_recommendation); 