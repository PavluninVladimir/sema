package storage

// S3-совместимое хранилище: Yandex Object Storage, MinIO, Backblaze — один
// клиент (minio-go, чистый Go). Ключи приходят из хранилища секретов Сёмы,
// в справочнике только их имена.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint string `json:"endpoint"` // host:port, без схемы
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	Prefix   string `json:"prefix"`
	SSL      bool   `json:"ssl"`
	// имена секретов с ключами
	AccessSecret string `json:"access_secret"`
	SecretSecret string `json:"secret_secret"`
}

type S3 struct {
	name   string
	conf   S3Config
	client *minio.Client
}

func NewS3(name string, conf S3Config, accessKey, secretKey string) (*S3, error) {
	endpoint := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(conf.Endpoint, "https://"), "http://"), "/")
	if endpoint == "" || conf.Bucket == "" {
		return nil, fmt.Errorf("нужны адрес и bucket")
	}
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: conf.SSL,
		Region: conf.Region,
	})
	if err != nil {
		return nil, err
	}
	return &S3{name: name, conf: conf, client: c}, nil
}

func (s *S3) Name() string       { return s.name }
func (s *S3) Kind() string       { return "s3" }
func (s *S3) Path(string) string { return "" }

func (s *S3) objectKey(key string) string {
	if s.conf.Prefix == "" {
		return key
	}
	return strings.TrimSuffix(s.conf.Prefix, "/") + "/" + key
}

func (s *S3) Put(key string, data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := s.client.PutObject(ctx, s.conf.Bucket, s.objectKey(key), bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
	return err
}

func (s *S3) Get(key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	obj, err := s.client.GetObject(ctx, s.conf.Bucket, s.objectKey(key), minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (s *S3) Delete(key string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return s.client.RemoveObject(ctx, s.conf.Bucket, s.objectKey(key), minio.RemoveObjectOptions{})
}

// Stat — сколько объектов и байт под префиксом (обход списка; на больших
// bucket'ах небыстро, поэтому зовётся только по кнопке).
func (s *S3) Stat() (files int, bytes int64) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for obj := range s.client.ListObjects(ctx, s.conf.Bucket, minio.ListObjectsOptions{Prefix: s.conf.Prefix, Recursive: true}) {
		if obj.Err != nil {
			break
		}
		files++
		bytes += obj.Size
	}
	return
}

// Check — соединение, bucket (создаём, если нет), запись и чтение пробы.
func (s *S3) Check() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ok, err := s.client.BucketExists(ctx, s.conf.Bucket)
	if err != nil {
		return "", fmt.Errorf("соединение: %w", err)
	}
	made := ""
	if !ok {
		if err := s.client.MakeBucket(ctx, s.conf.Bucket, minio.MakeBucketOptions{Region: s.conf.Region}); err != nil {
			return "", fmt.Errorf("bucket %q нет и создать не удалось: %w", s.conf.Bucket, err)
		}
		made = " (создан)"
	}
	probe := "sema-check"
	if err := s.Put(probe, []byte("ok")); err != nil {
		return "", fmt.Errorf("запись: %w", err)
	}
	data, err := s.Get(probe)
	if err != nil || string(data) != "ok" {
		return "", fmt.Errorf("чтение: %v", err)
	}
	_ = s.Delete(probe)
	return "bucket есть" + made + ", запись и чтение работают", nil
}
