package usage

import "math"

// DefaultBytesPerToken is the industry folk constant (~4 bytes/token for
// English and code) used when no ratio is configured. See
// docs/token-metering.md §3: bytes are the stored ground truth; tokens are a
// display-time estimate, honest to maybe ±20%.
const DefaultBytesPerToken = 4.0

// EstTokens converts bytes to an estimated token count with ratio
// bytesPerToken (0 or negative → DefaultBytesPerToken). Display-time only,
// never stored: keeping bytes as the column of record lets history re-render
// correctly under any future recalibration.
func EstTokens(bytes int64, bytesPerToken float64) int64 {
	if bytesPerToken <= 0 {
		bytesPerToken = DefaultBytesPerToken
	}
	if bytes <= 0 {
		return 0
	}
	return int64(math.Round(float64(bytes) / bytesPerToken))
}
