//go:build !darwin

package providerprocess

import "os"

// TrackDescendants adds the macOS native-provider fallback. Other platforms
// retain Configure's existing process-group/job cleanup; this is not a claim
// that their detached descendants have acquired the macOS coverage.
func TrackDescendants(*os.Process) (func() error, error) {
	return func() error { return nil }, nil
}
