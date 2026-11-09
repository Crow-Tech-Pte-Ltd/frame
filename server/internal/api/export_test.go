package api

// AdminRoutesForTest lists the registered admin API routes as
// (pattern, needs-session) pairs.
func AdminRoutesForTest(s *Server) []struct {
	Pattern string
	Private bool
} {
	out := make([]struct {
		Pattern string
		Private bool
	}, len(s.adminPatterns))
	for i, p := range s.adminPatterns {
		out[i].Pattern, out[i].Private = p.Pattern, p.Private
	}
	return out
}
