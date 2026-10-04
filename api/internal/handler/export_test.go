package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/justabill-org/justabill/db/model"
)

// ListCacheKey exposes listCacheKey to the handler_test package.
func ListCacheKey(prefix string, p model.ListParams) string { return listCacheKey(prefix, p) }

// ParseListParams exposes parseListParams to the handler_test package.
func ParseListParams(r *http.Request) (model.ListParams, error) { return parseListParams(r) }

// CacheJSON exposes cacheJSON to the handler_test package.
func (h *Handler) CacheJSON(ctx context.Context, key string, v any, ttl time.Duration) {
	h.cacheJSON(ctx, key, v, ttl)
}

// CacheSet exposes cacheSet to the handler_test package.
func (h *Handler) CacheSet(ctx context.Context, key, value string, ttl time.Duration) {
	h.cacheSet(ctx, key, value, ttl)
}
