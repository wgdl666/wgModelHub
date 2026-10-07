package config

import "testing"

func TestObjectStorageProviderContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cfg   ObjectStorageConfig
		valid bool
	}{
		{"disabled", ObjectStorageConfig{}, true},
		{"missing provider", ObjectStorageConfig{Bucket: "media", Region: "cn-shenzhen"}, false},
		{"unknown provider", ObjectStorageConfig{Provider: "minio", Bucket: "media", Region: "cn-shenzhen"}, false},
		{"oss", ObjectStorageConfig{Provider: "oss", Bucket: "media", Region: "cn-shenzhen", Endpoint: "https://oss-cn-shenzhen.aliyuncs.com", AccessKeyID: "ak", AccessKeySecret: "secret"}, true},
		{"oss missing identity", ObjectStorageConfig{Provider: "oss", Bucket: "media", Region: "cn-shenzhen"}, false},
		{"aws task role", ObjectStorageConfig{Provider: "s3", Bucket: "media", Region: "ap-southeast-1"}, true},
		{"aws partial credentials", ObjectStorageConfig{Provider: "s3", Bucket: "media", Region: "ap-southeast-1", AccessKeyID: "ak"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.validate(); (err == nil) != tc.valid {
				t.Fatalf("validate: %v", err)
			}
		})
	}
	previous := Config{ObjectStorage: ObjectStorageConfig{Provider: "oss"}}
	next := Config{ObjectStorage: ObjectStorageConfig{Provider: "s3"}}
	if len(RestartRequiredFields(previous, next)) == 0 {
		t.Fatal("SDK provider change must require restart")
	}
}
