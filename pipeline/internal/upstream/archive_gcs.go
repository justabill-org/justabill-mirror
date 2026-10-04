package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// GCSStore is the archive's store in production: one bucket, written with the pipeline's
// workload identity, which may create objects but not overwrite or delete them.
type GCSStore struct {
	client *storage.Client
	bucket *storage.BucketHandle
}

// NewGCSStore opens bucket with Application Default Credentials. opts are for tests (a
// fake endpoint, no auth).
func NewGCSStore(ctx context.Context, bucket string, opts ...option.ClientOption) (*GCSStore, error) {
	c, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("upstream archive: storage client: %w", err)
	}
	return &GCSStore{client: c, bucket: c.Bucket(bucket)}, nil
}

// Put implements [ArchiveStore]. The body is already gzipped, so the object is stored with
// Content-Encoding gzip, and GCS serves it decompressed to readers that don't ask for gzip.
// The write only succeeds if the name is new, which also makes the upload safe to retry.
func (s *GCSStore) Put(ctx context.Context, obj ArchiveObject) error {
	w := s.bucket.Object(obj.Name).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
	w.ContentType = obj.ContentType
	w.ContentEncoding = "gzip"
	w.Metadata = obj.Metadata
	// One chunk just larger than the body: the upload is one request, and the writer keeps
	// the buffer it needs to retry it (ChunkSize 0 would turn retries off).
	w.ChunkSize = len(obj.Body) + 1
	if _, err := w.Write(obj.Body); err != nil {
		_ = w.Close()
		return putError(err)
	}
	return putError(w.Close())
}

// Close closes the storage client.
func (s *GCSStore) Close() error {
	return s.client.Close()
}

func putError(err error) error {
	if err == nil {
		return nil
	}
	if gerr, ok := errors.AsType[*googleapi.Error](err); ok && gerr.Code == http.StatusPreconditionFailed {
		return ErrArchiveExists
	}
	return err
}
