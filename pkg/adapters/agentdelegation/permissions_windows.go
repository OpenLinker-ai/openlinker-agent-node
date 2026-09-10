//go:build windows

package agentdelegation

import "os"

func delegationSocketMode(_, _ string) (os.FileMode, error) { return 0o600, nil }
