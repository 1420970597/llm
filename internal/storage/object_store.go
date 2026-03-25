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
  object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
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
