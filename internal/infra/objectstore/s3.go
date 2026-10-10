package objectstore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	oss "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
	osscredentials "github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss/credentials"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/wgdl666/wgModelHub/config"
)

// S3 把账本图片和视频写到当前环境的对象存储。URI 只作为账本里的地址。
type S3 struct {
	client *s3.Client
	oss    *oss.Client
	bucket string
}

func New(ctx context.Context, cfg config.ObjectStorageConfig) (*S3, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	// 同一份桶配置保留原 endpoint，按 provider 使用对应厂商的签名和默认寻址规则。
	if cfg.Provider == "oss" {
		client := oss.NewClient(oss.LoadDefaultConfig().WithHttpClient(&http.Client{Timeout: 120 * time.Second}).WithRegion(strings.TrimSpace(cfg.Region)).WithEndpoint(strings.TrimSpace(cfg.Endpoint)).WithCredentialsProvider(osscredentials.NewStaticCredentialsProvider(strings.TrimSpace(cfg.AccessKeyID), strings.TrimSpace(cfg.AccessKeySecret))))
		return &S3{oss: client, bucket: strings.TrimSpace(cfg.Bucket)}, nil
	}
	if cfg.Provider != "s3" {
		return nil, fmt.Errorf("unsupported object storage provider %q", cfg.Provider)
	}
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(strings.TrimSpace(cfg.Region)), awsconfig.WithHTTPClient(&http.Client{Timeout: 120 * time.Second})}
	// AWS 未配置静态凭据时保留任务角色凭据链。
	if cfg.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(strings.TrimSpace(cfg.AccessKeyID), strings.TrimSpace(cfg.AccessKeySecret), "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("object storage config: %w", err)
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if endpoint != "" {
			options.BaseEndpoint = aws.String(endpoint)
			// endpoint 不决定寻址方式；使用 AWS SDK 默认规则。
		}
	})
	return &S3{client: client, bucket: strings.TrimSpace(cfg.Bucket)}, nil
}

func (s *S3) Put(ctx context.Context, key, contentType string, body []byte) (string, error) {
	if s == nil || (s.client == nil && s.oss == nil) {
		return "", fmt.Errorf("object storage is not configured")
	}
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	if s.oss != nil {
		_, err := s.oss.PutObject(ctx, &oss.PutObjectRequest{Bucket: oss.Ptr(s.bucket), Key: oss.Ptr(key), Body: bytes.NewReader(body), ContentType: oss.Ptr(contentType)})
		if err != nil {
			return "", err
		}
		// 账本 URI 是对象身份，沿用既有协议；不能持久化短期签名 URL。
		return "s3://" + s.bucket + "/" + key, nil
	}
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

// Get 按对象键读回已归档媒体。评测页要展示原图，不能把存储凭据交给浏览器。
func (s *S3) Get(ctx context.Context, key string) (string, []byte, error) {
	if s == nil || (s.client == nil && s.oss == nil) {
		return "", nil, fmt.Errorf("object storage is not configured")
	}
	key = strings.TrimLeft(strings.TrimSpace(key), "/")
	if s.oss != nil {
		result, err := s.oss.GetObject(ctx, &oss.GetObjectRequest{Bucket: oss.Ptr(s.bucket), Key: oss.Ptr(key)})
		if err != nil {
			return "", nil, err
		}
		defer result.Body.Close()
		body, err := io.ReadAll(result.Body)
		if err != nil {
			return "", nil, err
		}
		contentType := ""
		if result.ContentType != nil {
			contentType = *result.ContentType
		}
		return contentType, body, nil
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return "", nil, err
	}
	defer out.Body.Close()
	body, err := io.ReadAll(out.Body)
	if err != nil {
		return "", nil, err
	}
	contentType := ""
	if out.ContentType != nil {
		contentType = *out.ContentType
	}
	return contentType, body, nil
}
