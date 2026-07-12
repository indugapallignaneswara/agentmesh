package workspace

// clampLimit normalises a caller-supplied list limit: <=0 takes the default,
// anything above the ceiling is capped.
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	default:
		return limit
	}
}

// capList truncates a result set to limit and reports whether anything was
// dropped. Truncation is never silent: every list surface returns the flag so
// a caller can page or narrow its filter rather than quietly miss rows.
func capList[T any](items []T, limit int) ([]T, bool) {
	limit = clampLimit(limit)
	if len(items) <= limit {
		return items, false
	}
	return items[:limit], true
}
