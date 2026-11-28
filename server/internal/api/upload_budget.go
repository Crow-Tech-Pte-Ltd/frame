package api

import "sync"

// maxConcurrentUploads is how many uploads (Frames, avatars) one member may
// have in flight at once; each spools up to max_upload_bytes to disk.
const maxConcurrentUploads = 8

// Keep concurrent staging bounded even when many members upload together.
// A server configured for larger uploads can still stage one maximum-size upload.
const uploadStagingBudget = 512 << 20

// uploadGate counts uploads in flight and reserves the most bytes they can stage.
type uploadGate struct {
	mu       sync.Mutex
	n        map[string]int
	reserved int64
}

// acquire reserves an upload slot and staging space (429 when either is full).
// Call the returned function after cleaning up staged data.
func (g *uploadGate) acquire(userID string, bytes int64) (func(), error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n[userID] >= maxConcurrentUploads || g.reserved+bytes > max(uploadStagingBudget, bytes) {
		return nil, rateLimited(5)
	}
	if g.n == nil {
		g.n = map[string]int{}
	}
	g.n[userID]++
	g.reserved += bytes
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.reserved -= bytes
			if g.n[userID]--; g.n[userID] <= 0 {
				delete(g.n, userID)
			}
		})
	}, nil
}
