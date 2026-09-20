package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Profile struct {
	Endpoint     string
	AccessKeyID  string
	SecretKey    string
	Bucket       string
	Region       string
	UsePathStyle bool
}

type ObjectStore struct {
	client *minio.Client
	bucket string
	region string
}

func New(profile Profile) (*ObjectStore, error) {
	endpoint := strings.TrimPrefix(strings.TrimPrefix(profile.Endpoint, "http://"), "https://")
	secure := strings.HasPrefix(profile.Endpoint, "https://")
	options := &minio.Options{
		Creds:  credentials.NewStaticV4(profile.AccessKeyID, profile.SecretKey, ""),
		Secure: secure,
		Region: profile.Region,
	}
	if profile.UsePathStyle {
		options.BucketLookup = minio.BucketLookupPath
	}
	client, err := minio.New(endpoint, options)
	if err != nil {
		return nil, err
	}
	return &ObjectStore{client: client, bucket: profile.Bucket, region: profile.Region}, nil
}

func (s *ObjectStore) EnsureBucket(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region})
}

func (s *ObjectStore) PutJSON(ctx context.Context, key string, payload any) (string, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.PutBytes(ctx, key, body, "application/json")
}

func (s *ObjectStore) PutBytes(ctx context.Context, key string, payload []byte, contentType string) (string, error) {
	if err := s.EnsureBucket(ctx); err != nil {
		return "", err
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(payload), int64(len(payload)), minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("s3://%s/%s", s.bucket, key), nil
}

func (s *ObjectStore) ReadBytes(ctx context.Context, key string) ([]byte, error) {
	return s.ReadBytesFromBucket(ctx, s.bucket, key)
}

// ReadBytesFromBucket 从**指定 bucket** 读取对象，而不是用 Profile 里的默认 bucket。
//
// 为什么需要它（issue #136）：工件表把对象位置记在 `object_key` 里，形如
// `s3://<bucket>/<path>` —— **bucket 是工件自身携带的信息**。而下载此前用
// `ResolveStorageProfile` 解析出的「当前默认存储配置」的 bucket 去读，
// 于是管理员一旦切换过默认存储，此前用旧 bucket 导出的工件就永久 500：
//
//	当前默认存储: bucket=llm-factory-local
//	artifact#16 objectKey=s3://llm-factory-dev/...   -> 500 The specified key does not exist
//	（而 llm-minio-1 里 /data/llm-factory-dev/... 对象确实存在）
//
// 「结果存储」是管理员可正常使用的功能，因此这个缺陷会让历史工件在一次配置变更后
// 全部不可下载，且界面只显示「服务暂时不可用」，无法判断原因。
func (s *ObjectStore) ReadBytesFromBucket(ctx context.Context, bucket, key string) ([]byte, error) {
	if strings.TrimSpace(bucket) == "" {
		bucket = s.bucket
	}
	object, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()
	return io.ReadAll(object)
}

func (s *ObjectStore) ReadJSON(ctx context.Context, key string, target any) error {
	content, err := s.ReadBytes(ctx, key)
	if err != nil {
		return err
	}
	return json.Unmarshal(content, target)
}

func ParseEndpoint(raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, err
	}
	return parsed.Host, parsed.Scheme == "https", nil
}
