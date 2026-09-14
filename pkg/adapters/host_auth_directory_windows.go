package adapters

import (
	"errors"
)

func prepareHostAuthLockPath(_ []string, _ string) (string, error) {
	return "", errors.New("native host-auth admission requires macOS or Linux")
}
