create table if not exists candidates
(
    candidate_id     char(36)                                 not null
        primary key,
    primary_name     varchar(255)                             null,
    primary_phone    varchar(50)                              null,
    primary_email    varchar(255)                             null,
    gender           varchar(10)                              null,
    birth_date       date                                     null,
    current_location varchar(255)                             null,
    profile_summary  text                                     null,
    created_at       datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at       datetime(6) default CURRENT_TIMESTAMP(6) null,
    constraint idx_candidates_primary_email_unique
        unique (primary_email),
    constraint idx_candidates_primary_phone_unique
        unique (primary_phone)
);

create table if not exists interview_evaluations
(
    evaluation_id          bigint unsigned auto_increment
        primary key,
    candidate_id           char(36)                                 not null,
    job_id                 char(36)                                 not null,
    submission_uuid        char(36)                                 null,
    interviewer_id         char(36)                                 not null,
    interview_round        varchar(100)                             not null,
    interview_date         datetime(6)                              not null,
    overall_rating         bigint                                   null,
    evaluation_summary     text                                     null,
    strengths              text                                     null,
    weaknesses             text                                     null,
    specific_skill_ratings json                                     null,
    hiring_recommendation  varchar(50)                              null,
    notes                  text                                     null,
    created_at             datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at             datetime(6) default CURRENT_TIMESTAMP(6) null
);

create index idx_ie_candidate_id
    on interview_evaluations (candidate_id);

create fulltext index idx_ie_feedback_ft
    on interview_evaluations (evaluation_summary, strengths, weaknesses);

create index idx_ie_interview_date
    on interview_evaluations (interview_date);

create index idx_ie_interviewer_id
    on interview_evaluations (interviewer_id);

create index idx_ie_job_id
    on interview_evaluations (job_id);

create index idx_ie_job_recommendation
    on interview_evaluations (job_id, hiring_recommendation);

create index idx_ie_submission_uuid
    on interview_evaluations (submission_uuid);

create table if not exists interviewers
(
    interviewer_id char(36)                                 not null
        primary key,
    user_id        char(36)                                 null,
    name           varchar(255)                             not null,
    email          varchar(255)                             not null,
    department     varchar(255)                             null,
    title          varchar(255)                             null,
    is_active      tinyint(1)  default 1                    null,
    created_at     datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at     datetime(6) default CURRENT_TIMESTAMP(6) null,
    constraint uni_interviewers_email
        unique (email),
    constraint uni_interviewers_user_id
        unique (user_id)
);

create index idx_interviewers_is_active
    on interviewers (is_active);

create table if not exists job_submission_matches
(
    match_id                  bigint unsigned auto_increment
        primary key,
    submission_uuid           char(36)                                 not null,
    job_id                    char(36)                                 not null,
    llm_match_score           bigint                                   null,
    llm_match_highlights_json json                                     null,
    llm_potential_gaps_json   json                                     null,
    llm_resume_summary_for_jd text                                     null,
    vector_match_score        double                                   null,
    vector_score_details_json json                                     null,
    overall_calculated_score  double                                   null,
    evaluation_status         varchar(50) default 'PENDING'            null,
    evaluated_at              datetime(6)                              null,
    hr_feedback_status        varchar(50)                              null,
    hr_feedback_notes         text                                     null,
    created_at                datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at                datetime(6) default CURRENT_TIMESTAMP(6) null,
    constraint idx_jsm_submission_job_unique
        unique (submission_uuid, job_id)
);

create index idx_jsm_evaluation_status
    on job_submission_matches (evaluation_status);

create index idx_jsm_hr_feedback_status
    on job_submission_matches (hr_feedback_status);

create index idx_jsm_job_id_overall_score
    on job_submission_matches (job_id asc, overall_calculated_score desc);

create index idx_jsm_submission_uuid
    on job_submission_matches (submission_uuid);

create table if not exists jobs
(
    job_id                       char(36)                                 not null
        primary key,
    job_title                    varchar(255)                             not null,
    department                   varchar(255)                             null,
    location                     varchar(255)                             null,
    job_description_text         text                                     not null,
    structured_requirements_json json                                     null,
    jd_skills_keywords_json      json                                     null,
    status                       varchar(50) default 'ACTIVE'             null,
    created_by_user_id           char(36)                                 null,
    created_at                   datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at                   datetime(6) default CURRENT_TIMESTAMP(6) null
);

create table if not exists job_vectors
(
    job_id                  char(36)                                 not null
        primary key,
    vector_representation   mediumblob                               not null,
    embedding_model_version varchar(100)                             not null,
    created_at              datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at              datetime(6) default CURRENT_TIMESTAMP(6) null on update CURRENT_TIMESTAMP(6),
    constraint fk_job_vectors_job_id
        foreign key (job_id) references jobs (job_id)
            on delete cascade
)
    comment '存储岗位的向量表示及其模型版本。';

create fulltext index idx_jobs_description_ft
    on jobs (job_description_text);

create index idx_jobs_status
    on jobs (status);

create table if not exists outbox_messages
(
    id                 bigint auto_increment
        primary key,
    aggregate_id       varchar(36)                              not null,
    event_type         varchar(255)                             not null,
    payload            json                                     not null,
    target_exchange    varchar(255)                             not null,
    target_routing_key varchar(255)                             not null,
    status             varchar(20) default 'PENDING'            not null,
    retry_count        int         default 0                    null,
    created_at         datetime(6) default CURRENT_TIMESTAMP(6) null,
    processed_at       datetime(6)                              null,
    error_message      text                                     null
)
    charset = utf8mb4;

create index idx_outbox_status_created_at
    on outbox_messages (status, created_at);

create table if not exists resume_submission_chunks
(
    chunk_db_id            bigint unsigned auto_increment
        primary key,
    submission_uuid        char(36)                                 not null,
    chunk_id_in_submission bigint                                   not null,
    chunk_type             varchar(50)                              not null,
    chunk_title            text                                     null,
    chunk_content_text     text                                     not null,
    created_at             datetime(6) default CURRENT_TIMESTAMP(6) null,
    point_id               varchar(255)                             null,
    constraint idx_rsc_submission_chunk_id
        unique (submission_uuid, chunk_id_in_submission)
);

create fulltext index idx_rsc_chunk_ft
    on resume_submission_chunks (chunk_title, chunk_content_text);

create index idx_rsc_chunk_type
    on resume_submission_chunks (chunk_type);

create index idx_rsc_point_id
    on resume_submission_chunks (point_id);

create index idx_rsc_submission_uuid
    on resume_submission_chunks (submission_uuid);

create table if not exists resume_submissions
(
    submission_uuid        char(36)                                 not null
        primary key,
    candidate_id           char(36)                                 null,
    submission_timestamp   datetime(6) default CURRENT_TIMESTAMP(6) null,
    source_channel         varchar(100)                             null,
    target_job_id          char(36)                                 null,
    original_filename      varchar(255)                             null,
    original_file_path_oss varchar(1024)                            null,
    parsed_text_path_oss   varchar(1024)                            null,
    raw_text_md5           char(32)                                 null,
    similarity_hash        char(16)                                 null,
    llm_parsed_basic_info  json                                     null,
    llm_resume_identifier  varchar(255)                             null,
    processing_status      varchar(50) default 'PENDING_PARSING'    null,
    parser_version         varchar(50)                              null,
    created_at             datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at             datetime(6) default CURRENT_TIMESTAMP(6) null,
    qdrant_point_ids       json                                     null,
    constraint idx_rs_similarity_hash
        unique (similarity_hash),
    constraint fk_rs_candidate_id
        foreign key (candidate_id) references candidates (candidate_id)
            on delete set null
);

create index idx_rs_candidate_id
    on resume_submissions (candidate_id);

create index idx_rs_job_status_time
    on resume_submissions (target_job_id asc, processing_status asc, submission_timestamp desc);

create index idx_rs_processing_status
    on resume_submissions (processing_status);

create index idx_rs_raw_text_md5
    on resume_submissions (raw_text_md5);

create index idx_rs_submission_timestamp
    on resume_submissions (submission_timestamp);

create index idx_rs_target_job_id
    on resume_submissions (target_job_id);

create table if not exists reviewed_resumes
(
    review_id       bigint auto_increment comment '审阅记录的自增主键'
        primary key,
    job_id          char(36)                                 not null,
    hr_id           char(36)                                 not null,
    text_md5        char(32)                                 not null,
    submission_uuid char(36)                                 null,
    action          varchar(50)                              not null,
    reason_text     text                                     null,
    idempotency_key char(36)                                 null,
    version         bigint      default 1                    null,
    created_at      datetime(6) default CURRENT_TIMESTAMP(6) null,
    updated_at      datetime(6) default CURRENT_TIMESTAMP(6) null on update CURRENT_TIMESTAMP(6),
    constraint uq_reviewed_idempotency_key
        unique (idempotency_key) comment '确保幂等键的唯一性',
    constraint uq_reviewed_job_hr_text_md5
        unique (job_id, hr_id, text_md5) comment '确保同一HR对同一岗位下的同一份简历内容只有一条审阅记录',
    constraint fk_reviewed_resumes_job_id
        foreign key (job_id) references jobs (job_id)
            on delete cascade,
    constraint fk_reviewed_resumes_submission_uuid
        foreign key (submission_uuid) references resume_submissions (submission_uuid)
            on delete set null
)
    comment '存储HR对特定简历内容在特定岗位下的审阅记录及反馈。';

create index idx_reviewed_job_text_md5
    on reviewed_resumes (job_id, text_md5)
    comment '方便查询特定岗位下某份简历的所有HR审阅情况';

