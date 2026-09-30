package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/wgdl666/wgModelHub/config"
)

// S3 把账本图片和视频写到当前环境的对象存储。URI 只作为账本里的地址。
type S3 struct {
	client *s3.Client
	bucket string
}

func New(ctx context.Context, cfg config.ObjectStorageConfig) (*S3, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(strings.TrimSpace(cfg.Region)),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(strings.TrimSpace(cfg.AccessKeyID), strings.TrimSpace(cfg.AccessKeySecret), "")),
	)
	if err != nil {
		return nil, fmt.Errorf("object storage config: %w", err)
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			// 自建端点和 OSS 兼容地址不保证虚拟主机域名，用 path-style。
			options.UsePathStyle = true
		}
	})
	return &S3{client: client, bucket: strings.TrimSpace(cfg.Bucket)}, nil
}

func (s *S3) Put(ctx context.Context, key, contentType string, body []byte) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("object storage is not configured")
	}
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	input := &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(body),
	}
	if strings.TrimSpace(contentType) != "" {
		input.ContentType = aws.String(contentType)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return "", err
	}
	return "s3://" + s.bucket + "/" + key, nil
}
