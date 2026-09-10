//go:build unix

package providerprocess

import (
	"os"
	"os/user"
	"strconv"
)

func effectiveUsername() string {
	identity, err := user.LookupId(strconv.Itoa(os.Geteuid()))
	if err != nil {
		return ""
	}
	return identity.Username
}
