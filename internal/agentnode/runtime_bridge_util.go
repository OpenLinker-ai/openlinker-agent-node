package agentnode

import (
	"errors"
	"fmt"

	openlinker "github.com/OpenLinker-ai/openlinker-go"
)

func validateDelegatedIdempotencyKey(key string) error {
	if len(key) == 0 {
		return errors.New("idempotency key is required for delegated Agent calls")
	}
	if len(key) > 255 || key[0] == ' ' || key[len(key)-1] == ' ' {
		return errors.New("idempotency key must contain 1 to 255 printable ASCII bytes without surrounding spaces")
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] > 0x7e {
			return errors.New("idempotency key must contain 1 to 255 printable ASCII bytes without surrounding spaces")
		}
	}
	return nil
}

func scrubRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	var runtimeErr *openlinker.Error
	if errors.As(err, &runtimeErr) {
		return fmt.Errorf("%s (HTTP %d)", runtimeErr.Code, runtimeErr.StatusCode)
	}
	return err
}
