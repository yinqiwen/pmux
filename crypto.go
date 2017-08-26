package pmux

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/salsa20"
)

const (
	CipherNone             = "none"
	CipherSalsa20          = "salsa20"
	CipherChacha20Poly1305 = "chacha20poly1305"
	CipherAES256GCM        = "aes256-gcm"
)

type cipherCodec interface {
	Encrypt(data []byte, counter uint64) ([]byte, error)
	Decrypt(data []byte, counter uint64) ([]byte, error)
}

type noneCodec struct {
	Stream cipher.Stream
}

func (s *noneCodec) Encrypt(data []byte, counter uint64) ([]byte, error) {
	return data, nil
}
func (s *noneCodec) Decrypt(data []byte, counter uint64) ([]byte, error) {
	return data, nil
}

var noneCipherCodec = &noneCodec{}

type ciperAEADCodec struct {
	aead     cipher.AEAD
	encnonce []byte
	decnonce []byte
}

func (s *ciperAEADCodec) Encrypt(data []byte, counter uint64) ([]byte, error) {
	binary.BigEndian.PutUint64(s.encnonce, counter)
	data = s.aead.Seal(data[:0], s.encnonce, data, nil)
	return data, nil
}
func (s *ciperAEADCodec) Decrypt(data []byte, counter uint64) ([]byte, error) {
	binary.BigEndian.PutUint64(s.decnonce, counter)
	return s.aead.Open(data[:0], s.decnonce, data, nil)
}

type salsa20Codec struct {
	salsa20Key [32]byte
	encnonce   []byte
	decnonce   []byte
}

func (s *salsa20Codec) Encrypt(data []byte, counter uint64) ([]byte, error) {
	binary.BigEndian.PutUint64(s.encnonce, counter)
	salsa20.XORKeyStream(data, data, s.encnonce, &s.salsa20Key)
	return data, nil
}
func (s *salsa20Codec) Decrypt(data []byte, counter uint64) ([]byte, error) {
	binary.BigEndian.PutUint64(s.decnonce, counter)
	salsa20.XORKeyStream(data, data, s.decnonce, &s.salsa20Key)
	return data, nil
}

type cipherInfo struct {
	keyLen         int
	newCipherCodec func(key []byte) (cipherCodec, error)
}

func newNoneCipher(key []byte) (cipherCodec, error) {
	return noneCipherCodec, nil
}

func newAESGCMCodec(key []byte) (cipherCodec, error) {
	block, err := aes.NewCipher(key)
	if nil != err {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if nil != err {
		return nil, err
	}

	codec := &ciperAEADCodec{
		aead:     aead,
		encnonce: make([]byte, aead.NonceSize()),
		decnonce: make([]byte, aead.NonceSize()),
	}
	return codec, nil
}

func newChacha20Poly1305Codec(key []byte) (cipherCodec, error) {
	aead, err := chacha20poly1305.New(key)
	if nil != err {
		return nil, err
	}
	codec := &ciperAEADCodec{
		aead:     aead,
		encnonce: make([]byte, aead.NonceSize()),
		decnonce: make([]byte, aead.NonceSize()),
	}
	return codec, nil
}

func newSalsa20Codec(key []byte) (cipherCodec, error) {
	codec := &salsa20Codec{
		encnonce: make([]byte, 24),
		decnonce: make([]byte, 24),
	}
	copy(codec.salsa20Key[:], key[:32])
	return codec, nil
}

var cipherMethodTable = map[string]*cipherInfo{
	CipherAES256GCM:        {32, newAESGCMCodec},
	CipherChacha20Poly1305: {32, newChacha20Poly1305Codec},
	CipherSalsa20:          {32, newSalsa20Codec},
	CipherNone:             {32, newNoneCipher},
}

func getCipher(method string, key []byte) (cipherCodec, error) {
	info, exist := cipherMethodTable[method]
	if !exist {
		return nil, ErrInvalidCipherMethod
	}
	if len(key) < info.keyLen {
		key = append(key, make([]byte, info.keyLen-len(key))...)
	} else {
		key = key[0:info.keyLen]
	}

	// if len(iv) < info.ivLen {
	// 	iv = append(key, make([]byte, info.ivLen-len(iv))...)
	// } else {
	// 	iv = iv[0:info.ivLen]
	// }
	return info.newCipherCodec(key)
}

type CryptoContext struct {
	Key    []byte
	cipher cipherCodec

	decryptCounter uint64
	encryptCounter uint64

	encryptLenKey []byte
	decryptLenKey []byte
}

func (ctx *CryptoContext) decodeLength(x uint32) uint32 {
	//log.Printf("####%v %p", noneCipherCodec == ctx.cipher)
	if noneCipherCodec == ctx.cipher {
		return x
	}
	if len(ctx.decryptLenKey) != 10 {
		panic("invalid skip32 key size")
	}
	binary.BigEndian.PutUint64(ctx.decryptLenKey[2:], ctx.decryptCounter)
	return skip32Unobfus(x, ctx.decryptLenKey)
}

func (ctx *CryptoContext) encodeLength(x uint32) uint32 {
	if noneCipherCodec == ctx.cipher {
		return x
	}
	if len(ctx.encryptLenKey) != 10 {
		panic("invalid skip32 key size")
	}
	binary.BigEndian.PutUint64(ctx.encryptLenKey[2:], ctx.encryptCounter)
	return skip32Obfus(x, ctx.encryptLenKey)
}

func (ctx *CryptoContext) incDecryptCounter() {
	ctx.decryptCounter++
}
func (ctx *CryptoContext) incEncryptCounter() {
	ctx.encryptCounter++
}

func (ctx *CryptoContext) encodeData(data []byte) ([]byte, error) {
	p, err := ctx.cipher.Encrypt(data, ctx.encryptCounter)
	return p, err
}

func (ctx *CryptoContext) decodeData(data []byte) ([]byte, error) {
	p, err := ctx.cipher.Decrypt(data, ctx.decryptCounter)
	return p, err
}

func NewCryptoContext(method string, key []byte, counter uint64) (*CryptoContext, error) {
	ctx := &CryptoContext{
		Key: key,
		//InitialIV: iv,
	}
	codec, err := getCipher(method, key)
	if nil != err {
		return nil, err
	}
	ctx.cipher = codec
	ctx.encryptLenKey = make([]byte, 10)
	ctx.decryptLenKey = make([]byte, 10)
	ctx.encryptCounter = counter
	ctx.decryptCounter = counter
	copy(ctx.encryptLenKey[0:2], key[0:2])
	copy(ctx.decryptLenKey[0:2], key[0:2])
	return ctx, nil
}
