package pocket48

import (
	"crypto/aes"
	"encoding/base64"
	"fmt"
)

const pocketPasswordSecret = "7yy4tjcw12aipo2c"

// EncryptPassword reproduces Pocket48 Android 7.1.43's
// AES/ECB/PKCS5Padding + Base64.NO_WRAP password encoding.
func EncryptPassword(password string) (string, error) {
	block, err := aes.NewCipher([]byte(pocketPasswordSecret))
	if err != nil {
		return "", fmt.Errorf("create Pocket48 password cipher: %w", err)
	}

	blockSize := block.BlockSize()
	padding := blockSize - len([]byte(password))%blockSize
	plain := append([]byte(password), make([]byte, padding)...)
	for i := len(plain) - padding; i < len(plain); i++ {
		plain[i] = byte(padding)
	}

	encrypted := make([]byte, len(plain))
	for offset := 0; offset < len(plain); offset += blockSize {
		block.Encrypt(encrypted[offset:offset+blockSize], plain[offset:offset+blockSize])
	}
	return base64.StdEncoding.EncodeToString(encrypted), nil
}
