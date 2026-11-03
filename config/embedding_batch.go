package config

import "fmt"

// Resolved 为未配置过的新字段设置批量容量默认值，不改变模型、地址或凭据。
func (c CohereEmbeddingProviderConfig) Resolved() CohereEmbeddingProviderConfig {
	if c.MaxBatchSize == 0 {
		c.MaxBatchSize = 96
	}
	// 512条1024维 float 向量按每个数32字节保守预算约16 MiB，低于统一64 MiB消息上限。
	if c.MaxRequestTexts == 0 {
		c.MaxRequestTexts = DefaultEmbeddingRequestTexts
	}
	if c.MaxRequestTextBytes == 0 {
		c.MaxRequestTextBytes = DefaultEmbeddingRequestTextBytes
	}
	return c
}

// ValidateExecutionLimits 校验供应商单批能力与批量请求容量；合法大请求由 Provider 分片处理。
func (c CohereEmbeddingProviderConfig) ValidateExecutionLimits() error {
	c = c.Resolved()
	if c.MaxBatchSize < 1 || c.MaxBatchSize > 96 {
		return fmt.Errorf("cohere embedding max_batch_size must be 1..96")
	}
	return validateEmbeddingRequestLimits("cohere", c.MaxRequestTexts, c.MaxRequestTextBytes)
}

const (
	DefaultEmbeddingRequestTexts     = 512
	DefaultEmbeddingRequestTextBytes = 1 << 20
)

func (c DashScopeEmbeddingProviderConfig) Resolved() DashScopeEmbeddingProviderConfig {
	if c.MaxBatchSize == 0 {
		c.MaxBatchSize = 20
	}
	if c.MaxRequestTexts == 0 {
		c.MaxRequestTexts = DefaultEmbeddingRequestTexts
	}
	if c.MaxRequestTextBytes == 0 {
		c.MaxRequestTextBytes = DefaultEmbeddingRequestTextBytes
	}
	return c
}

func (c DashScopeEmbeddingProviderConfig) ValidateExecutionLimits() error {
	c = c.Resolved()
	if c.MaxBatchSize < 1 || c.MaxBatchSize > 20 {
		return fmt.Errorf("dashscope embedding max_batch_size must be 1..20")
	}
	return validateEmbeddingRequestLimits("dashscope", c.MaxRequestTexts, c.MaxRequestTextBytes)
}

func validateEmbeddingRequestLimits(name string, maxTexts, maxTextBytes int) error {
	// 1024 条向量的序列化上限仍需给 RPC 包装与账本留出空间。
	if maxTexts < 1 || maxTexts > 1024 {
		return fmt.Errorf("%s embedding max_request_texts must be 1..1024", name)
	}
	if maxTextBytes < 1 || maxTextBytes > 4<<20 {
		return fmt.Errorf("%s embedding max_request_text_bytes must be 1..4194304", name)
	}
	return nil
}
