ALTER TABLE resume_submission_chunks
ADD COLUMN point_id VARCHAR(255) NULL,
ADD INDEX idx_rsc_point_id (point_id); 