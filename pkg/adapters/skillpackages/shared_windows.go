package skillpackages

import "errors"

func CheckSharedCache(_ string, _ int) error {
	return errors.New("shared group skill caches are unsupported on Windows")
}
