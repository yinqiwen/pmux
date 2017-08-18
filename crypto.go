package pmux

import (
	"crypto/aes"
	"crypto/cipher"
)

const (
	NoneCipher = iota
	AESCTRCipher
	Chacha20Poly1305Cipher
)

type NoneCipherStream struct {
}

func (n *NoneCipherStream) XORKeyStream(dst, src []byte) {
}

func getCipherStream(method uint16, secretKey []byte, iv []byte) (cipher.Stream, error) {
	switch method {
	case NoneCipher:
		return &NoneCipherStream{}, nil
	case AESCTRCipher:
		aesblock, err := aes.NewCipher(secretKey)
		if nil != err {
			return nil, err
		}
		return cipher.NewCTR(aesblock, iv), nil
	case Chacha20Poly1305Cipher:
		//chacha20poly1305.New(secretKey)
	default:
		return nil, ErrInvalidCipherMethod
	}
}
