package config

import "fmt"

// Resolved 为未配置过的新字段设置执行预算，不改变模型、地址或凭据。
func (c CohereEmbeddingProviderConfig) Resolved() CohereEmbeddingProviderConfig {
	if c.MaxBatchSize == 0 {
		c.MaxBatchSize = 96
	}
	if c.MaxConcurrency == 0 {
		c.MaxConcurrency = 4
	}
	// 512条1024维 float 向量按每个数32字节保守预算约16 MiB，低于统一64 MiB消息上限。
	if c.MaxRequestTexts == 0 {
		c.MaxRequestTexts = 512
	}
	if c.MaxRequestTextBytes == 0 {
		c.MaxRequestTextBytes = 1 << 20
	}
	if c.RequestTimeoutSeconds == 0 {
		c.RequestTimeoutSeconds = 30
	}
	return c
}

// ValidateExecutionLimits 校验供应商单批能力与整单资源预算；合法大请求由 Provider 分片处理。
func (c CohereEmbeddingProviderConfig) ValidateExecutionLimits() error {
	c = c.Resolved()
	if c.MaxBatchSize < 1 || c.MaxBatchSize > 96 {
		return fmt.Errorf("cohere embedding max_batch_size must be 1..96")
	}
	if c.MaxConcurrency < 1 || c.MaxConcurrency > 64 {
		return fmt.Errorf("cohere embedding max_concurrency must be 1..64")
	}
	// 配置扩大到1024条时保守响应预算约32 MiB，仍给RPC包装和账本序列化留空间。
	if c.MaxRequestTexts < 1 || c.MaxRequestTexts > 1024 {
		return fmt.Errorf("cohere embedding max_request_texts must be 1..1024")
	}
	if c.MaxRequestTextBytes < 1 || c.MaxRequestTextBytes > 4<<20 {
		return fmt.Errorf("cohere embedding max_request_text_bytes must be 1..4194304")
	}
	if c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 120 {
		return fmt.Errorf("cohere embedding request_timeout_seconds must be 1..120")
	}
	return nil
}
