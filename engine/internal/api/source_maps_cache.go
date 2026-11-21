package api

import (
	"context"
	"sync"
	"time"
)

// sourceMaps remembers for a minute which brands have no source maps, so
// opening an issue of a brand that never uploaded one costs no query per
// stack line. An upload or a delete forgets the brand at once on this
// server; another server learns within the minute.
var sourceMaps = &sourceMapBrands{known: map[string]sourceMapBrand{}}

type sourceMapBrand struct {
	any bool
	at  time.Time
}

type sourceMapBrands struct {
	mu    sync.Mutex
	known map[string]sourceMapBrand
}

func (c *sourceMapBrands) forget(brand string) {
	c.mu.Lock()
	delete(c.known, brand)
	c.mu.Unlock()
}

func (c *sourceMapBrands) any(ctx context.Context, s *Server, ws, brand string) bool {
	c.mu.Lock()
	k, ok := c.known[brand]
	c.mu.Unlock()
	if ok && time.Since(k.at) < time.Minute {
		return k.any
	}
	var any bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM source_maps WHERE workspace_id = $1 AND brand_id = $2)`, ws, brand).Scan(&any); err != nil {
		return false
	}
	c.mu.Lock()
	if len(c.known) > 10000 {
		c.known = map[string]sourceMapBrand{}
	}
	c.known[brand] = sourceMapBrand{any: any, at: time.Now()}
	c.mu.Unlock()
	return any
}
