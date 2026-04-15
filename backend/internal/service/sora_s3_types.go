package service

// SoraS3Settings Sora S3 单配置视图（兼容旧接口语义）
type SoraS3Settings struct {
	Enabled                   bool   `json:"enabled"`
	Endpoint                  string `json:"endpoint"`
	Region                    string `json:"region"`
	Bucket                    string `json:"bucket"`
	AccessKeyID               string `json:"access_key_id"`
	SecretAccessKey           string `json:"secret_access_key,omitempty"`
	SecretAccessKeyConfigured bool   `json:"secret_access_key_configured"`
	Prefix                    string `json:"prefix"`
	ForcePathStyle            bool   `json:"force_path_style"`
	CDNURL                    string `json:"cdn_url"`
	DefaultStorageQuotaBytes  int64  `json:"default_storage_quota_bytes"`
}

// SoraS3Profile Sora S3 多配置项
type SoraS3Profile struct {
	ProfileID                 string `json:"profile_id"`
	Name                      string `json:"name"`
	IsActive                  bool   `json:"is_active"`
	Enabled                   bool   `json:"enabled"`
	Endpoint                  string `json:"endpoint"`
	Region                    string `json:"region"`
	Bucket                    string `json:"bucket"`
	AccessKeyID               string `json:"access_key_id"`
	SecretAccessKey           string `json:"secret_access_key,omitempty"`
	SecretAccessKeyConfigured bool   `json:"secret_access_key_configured"`
	Prefix                    string `json:"prefix"`
	ForcePathStyle            bool   `json:"force_path_style"`
	CDNURL                    string `json:"cdn_url"`
	DefaultStorageQuotaBytes  int64  `json:"default_storage_quota_bytes"`
	UpdatedAt                 string `json:"updated_at"`
}

// SoraS3ProfileList Sora S3 多配置列表
type SoraS3ProfileList struct {
	ActiveProfileID string          `json:"active_profile_id"`
	Items           []SoraS3Profile `json:"items"`
}
