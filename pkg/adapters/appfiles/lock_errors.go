package appfiles

import "errors"

// ErrLockBusy identifies ownership contention without changing the legacy text.
// It does not classify permission, path validation or other I/O errors as busy.
var ErrLockBusy = errors.New("Agent state is already serving another Runtime Worker")
