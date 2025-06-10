SELECT
    submission_uuid,
    chunk_type,
    MATCH(chunk_title, chunk_content_text)
          AGAINST('+Kubernetes +Go' IN BOOLEAN MODE) AS score
FROM resume_submission_chunks
WHERE MATCH(chunk_title, chunk_content_text)
            AGAINST('+Kubernetes +Go' IN BOOLEAN MODE)
ORDER BY score DESC
LIMIT 10;
