package embedkv

import "errors"

var (
	ErrInvalidHeader    = errors.New("embedkv: invalid storage header")
	ErrInvalidBlock     = errors.New("embedkv: invalid block")
	ErrCRCMismatch      = errors.New("embedkv: CRC32 mismatch")
	ErrKeyNotFound      = errors.New("embedkv: key not found")
	ErrStorageFull      = errors.New("embedkv: storage full")
	ErrCorruptRecord    = errors.New("embedkv: corrupt record")
	ErrInvalidBlockSize = errors.New("embedkv: invalid block size")
	ErrSizeMismatch     = errors.New("embedkv: storage size mismatch")
)
