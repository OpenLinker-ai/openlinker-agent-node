//go:build windows

package providerprocess

import "os/user"

func effectiveUsername() string {
	identity, err := user.Current()
	if err != nil {
		return ""
	}
	return identity.Username
}
