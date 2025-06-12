package constants

// Redis Key 前缀和格式常量
// 使用统一的命名规范: app:{module}:{entity}:{unique_id}
const (
	// AppPrefix 是所有Redis Key的统一应用前缀
	AppPrefix = "app"

	// SearchModulePrefix 搜索模块
	SearchModulePrefix = "search"
	// JobModulePrefix 岗位模块
	JobModulePrefix = "job"
	// FileModulePrefix 文件模块
	FileModulePrefix = "file"

	// EntitySession 搜索会话实体
	EntitySession = "session"
	// EntityLock 分布式锁实体
	EntityLock = "lock"
	// EntityText 文本实体
	EntityText = "text"
	// EntityVector 向量实体
	EntityVector = "vector"
	// EntityDedupSet 去重集合实体
	EntityDedupSet = "dedup_set"
	// EntityMD5ToUUID MD5到UUID的映射实体
	EntityMD5ToUUID = "md5_to_uuid"

	// KeySearchSession 搜索会话缓存 (ZSET)
	// 格式: app:search:session:{jobID}:{hrID}
	KeySearchSession = AppPrefix + ":" + SearchModulePrefix + ":" + EntitySession + ":%s:%s"

	// KeySearchLock 搜索分布式锁 (STRING)
	// 格式: app:search:lock:{jobID}:{hrID}
	KeySearchLock = AppPrefix + ":" + SearchModulePrefix + ":" + EntityLock + ":%s:%s"

	// KeyJobDescriptionText JD文本缓存 (STRING)
	// 格式: app:job:text:{jobID}
	KeyJobDescriptionText = AppPrefix + ":" + JobModulePrefix + ":" + EntityText + ":%s"

	// KeyJobDescriptionVector JD向量缓存 (HASH)
	// 格式: app:job:vector:{jobID}
	KeyJobDescriptionVector = AppPrefix + ":" + JobModulePrefix + ":" + EntityVector + ":%s"

	// KeyFileMD5Set 文件MD5集合，用于快速去重 (SET)
	// 格式: app:file:dedup_set
	KeyFileMD5Set = AppPrefix + ":" + FileModulePrefix + ":" + EntityDedupSet

	// KeyFileMD5ToSubmissionUUID MD5到SubmissionUUID的映射 (STRING)
	// 格式: app:file:md5_to_uuid:{md5}
	KeyFileMD5ToSubmissionUUID = AppPrefix + ":" + FileModulePrefix + ":" + EntityMD5ToUUID + ":%s"

	// KeySearchSessionV2_A A/B测试使用的搜索会话缓存键 - A组（带短板惩罚）
	KeySearchSessionV2_A = AppPrefix + ":" + SearchModulePrefix + ":" + EntitySession + ":v2a:%s:%s"
	// KeySearchSessionV2_B A/B测试使用的搜索会话缓存键 - B组（不带短板惩罚）
	KeySearchSessionV2_B = AppPrefix + ":" + SearchModulePrefix + ":" + EntitySession + ":v2b:%s:%s"
)
