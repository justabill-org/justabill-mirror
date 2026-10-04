// Package gcsblob reads, writes and lists Cloud Storage objects named by gs:// URIs, for the
// Vertex AI batch path's input and output files (docs/design/198-corpus-resummarization.md).
package gcsblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

const scheme = "gs://"

// ErrBadURI is returned for a URI that isn't gs://<bucket>/<object>.
var ErrBadURI = errors.New("not a gs://bucket/object URI")

// Store reaches Cloud Storage with one client, for objects in any bucket it may use.
type Store struct {
	client *storage.Client
}

// New opens a Store with Application Default Credentials. opts are for tests (a fake endpoint,
// no auth).
func New(ctx context.Context, opts ...option.ClientOption) (*Store, error) {
	c, err := storage.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("storage client: %w", err)
	}
	return &Store{client: c}, nil
}

// Create returns a writer for a new object at uri. The object exists once Close returns nil;
// an object already there is replaced.
func (s *Store) Create(ctx context.Context, uri string) (io.WriteCloser, error) {
	obj, err := s.object(uri)
	if err != nil {
		return nil, err
	}
	w := obj.NewWriter(ctx)
	w.ContentType = "application/jsonl"
	return w, nil
}

// Open returns a reader for the object at uri.
func (s *Store) Open(ctx context.Context, uri string) (io.ReadCloser, error) {
	obj, err := s.object(uri)
	if err != nil {
		return nil, err
	}
	r, err := obj.NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", uri, err)
	}
	return r, nil
}

// List returns the URIs of the objects whose names start with prefix's object part, in name
// order.
func (s *Store) List(ctx context.Context, prefix string) ([]string, error) {
	bucket, name, err := ParseURI(prefix)
	if err != nil {
		return nil, err
	}
	it := s.client.Bucket(bucket).Objects(ctx, &storage.Query{Prefix: name})
	var uris []string
	for {
		attrs, nextErr := it.Next()
		if errors.Is(nextErr, iterator.Done) {
			return uris, nil
		}
		if nextErr != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, nextErr)
		}
		uris = append(uris, scheme+bucket+"/"+attrs.Name)
	}
}

// Close closes the storage client.
func (s *Store) Close() error {
	return s.client.Close()
}

func (s *Store) object(uri string) (*storage.ObjectHandle, error) {
	bucket, name, err := ParseURI(uri)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, fmt.Errorf("%w: %q has no object name", ErrBadURI, uri)
	}
	return s.client.Bucket(bucket).Object(name), nil
}

// ParseURI splits gs://<bucket>/<object> into its bucket and object name. The name may be empty
// (gs://bucket or gs://bucket/), which names the whole bucket as a prefix.
func ParseURI(uri string) (string, string, error) {
	rest, ok := strings.CutPrefix(uri, scheme)
	if !ok {
		return "", "", fmt.Errorf("%w: %q", ErrBadURI, uri)
	}
	bucket, name, _ := strings.Cut(rest, "/")
	if bucket == "" {
		return "", "", fmt.Errorf("%w: %q has no bucket", ErrBadURI, uri)
	}
	return bucket, name, nil
}
