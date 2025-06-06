CREATE TABLE outbox_messages (
    id BIGINT AUTO_INCREMENT PRIMARY KEY,
    aggregate_id VARCHAR(36) NOT NULL, -- 关联的业务ID，如 submission_uuid
    event_type VARCHAR(255) NOT NULL,
    payload JSON NOT NULL,
    target_exchange VARCHAR(255) NOT NULL,
    target_routing_key VARCHAR(255) NOT NULL,
    status VARCHAR(20) DEFAULT 'PENDING' NOT NULL,
    retry_count INT DEFAULT 0,
    created_at DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6),
    processed_at DATETIME(6) NULL,
    error_message TEXT,
    INDEX idx_outbox_status_created_at (status, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4; 