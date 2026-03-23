package crypto

import (
  "crypto/aes"
  "crypto/cipher"
  "crypto/rand"
  "encoding/base64"
  "fmt"
  "io"
)

type SecretBox struct {
  key []byte
}

func NewSecretBox(key string) (*SecretBox, error) {
  raw := []byte(key)
  if len(raw) < 32 {
    return nil, fmt.Errorf("encryption key must be at least 32 bytes")
  }
  return &SecretBox{key: raw[:32]}, nil
}

func (b *SecretBox) Encrypt(value string) (string, error) {
  if value == "" {
    return "", nil
  }

  block, err := aes.NewCipher(b.key)
  if err != nil {
    return "", err
  }

  gcm, err := cipher.NewGCM(block)
  if err != nil {
    return "", err
  }

  nonce := make([]byte, gcm.NonceSize())
  if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
    return "", err
  }

  ciphertext := gcm.Seal(nonce, nonce, []byte(value), nil)
  return base64.StdEncoding.EncodeToString(ciphertext), nil
}

func (b *SecretBox) Decrypt(value string) (string, error) {
  if value == "" {
    return "", nil
  }

  payload, err := base64.StdEncoding.DecodeString(value)
  if err != nil {
    return "", err
  }

  block, err := aes.NewCipher(b.key)
  if err != nil {
    return "", err
  }

  gcm, err := cipher.NewGCM(block)
  if err != nil {
    return "", err
  }

  if len(payload) < gcm.NonceSize() {
    return "", fmt.Errorf("ciphertext too short")
  }

  nonce := payload[:gcm.NonceSize()]
  plaintext, err := gcm.Open(nil, nonce, payload[gcm.NonceSize():], nil)
  if err != nil {
    return "", err
  }

  return string(plaintext), nil
}

func MaskSecret(value string) string {
  if value == "" {
    return ""
  }
  if len(value) <= 4 {
    return "****"
  }
  return "****" + value[len(value)-4:]
}
