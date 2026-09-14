// Package hasher cuida da detecção de mudança de arquivos: o par
// (size, mtime) é a checagem barata; o sha256 do conteúdo é a definitiva.
package hasher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
)

// Fingerprint é a assinatura barata de um arquivo, obtida só com stat.
type Fingerprint struct {
	Size    int64
	ModTime int64 // unix nanos
}

// FingerprintOf extrai a assinatura de um FileInfo.
func FingerprintOf(info fs.FileInfo) Fingerprint {
	return Fingerprint{Size: info.Size(), ModTime: info.ModTime().UnixNano()}
}

// Sum devolve o sha256 hex do conteúdo.
func Sum(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadAndHash lê o arquivo e devolve conteúdo e hash de uma vez, para que o
// indexador não leia o arquivo duas vezes.
func ReadAndHash(path string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}
	return data, Sum(data), nil
}
