// Package bucket is the object store the images live in: an S3-compatible
// bucket (Cloudflare R2 in production), reached through minio-go the way
// image-sync reaches it.
package bucket

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Store is what the pipeline needs of a bucket. Keys are bucket-relative
// ("weeb/posters/<id>"), with no leading slash.
type Store interface {
	Get(ctx context.Context, key string) (data []byte, contentType string, meta map[string]string, err error)
	Put(ctx context.Context, key string, data []byte, contentType string, meta map[string]string) error
	// Copy duplicates an object server-side, metadata included.
	Copy(ctx context.Context, srcKey, dstKey string) error
	Exists(ctx context.Context, key string) (bool, error)
	// List walks every object key under prefix.
	List(ctx context.Context, prefix string) <-chan Entry
}

type Entry struct {
	Key  string
	Size int64
	Err  error
}

type Config struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	UseSSL          bool
	Bucket          string
}

type Minio struct {
	client *minio.Client
	bucket string
}

func New(cfg Config) (*Minio, error) {
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, err
	}
	return &Minio{client: client, bucket: cfg.Bucket}, nil
}

func (m *Minio) Get(ctx context.Context, key string) ([]byte, string, map[string]string, error) {
	obj, err := m.client.GetObject(ctx, m.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, "", nil, err
	}
	defer obj.Close()
	info, err := obj.Stat()
	if err != nil {
		return nil, "", nil, err
	}
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, "", nil, err
	}
	return data, info.ContentType, lower(info.UserMetadata), nil
}

func (m *Minio) Put(ctx context.Context, key string, data []byte, contentType string, meta map[string]string) error {
	_, err := m.client.PutObject(ctx, m.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType:  contentType,
		UserMetadata: meta,
	})
	return err
}

func (m *Minio) Copy(ctx context.Context, srcKey, dstKey string) error {
	_, err := m.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: m.bucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: m.bucket, Object: srcKey})
	return err
}

func (m *Minio) Exists(ctx context.Context, key string) (bool, error) {
	_, err := m.client.StatObject(ctx, m.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).StatusCode == http.StatusNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (m *Minio) List(ctx context.Context, prefix string) <-chan Entry {
	out := make(chan Entry)
	go func() {
		defer close(out)
		for obj := range m.client.ListObjects(ctx, m.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
			if obj.Err != nil {
				out <- Entry{Err: obj.Err}
				return
			}
			if strings.HasSuffix(obj.Key, "/") {
				continue
			}
			select {
			case out <- Entry{Key: obj.Key, Size: obj.Size}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// S3 hands user metadata back with header casing; callers key on what was written.
func lower(meta map[string]string) map[string]string {
	out := make(map[string]string, len(meta))
	for k, v := range meta {
		out[strings.ToLower(k)] = v
	}
	return out
}
