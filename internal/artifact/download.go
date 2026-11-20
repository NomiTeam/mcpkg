package artifact

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxArtifactBytes = 256 << 20

func Write(dst string, reader io.Reader, digest string) (string, error) {
	file, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sha256Hash := sha256.New()
	sha512Hash := sha512.New()
	n, err := io.Copy(io.MultiWriter(file, sha256Hash, sha512Hash), io.LimitReader(reader, maxArtifactBytes+1))
	if err != nil {
		return "", err
	}
	if n == 0 || n > maxArtifactBytes {
		return "", fmt.Errorf("invalid download size: %d bytes", n)
	}
	sha256Sum := hex.EncodeToString(sha256Hash.Sum(nil))
	if digest != "" && !strings.EqualFold(digest, "sha256:"+sha256Sum) &&
		!strings.EqualFold(digest, "sha512:"+hex.EncodeToString(sha512Hash.Sum(nil))) {
		return "", fmt.Errorf("artifact checksum mismatch")
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	return sha256Sum, file.Close()
}

func Download(ctx context.Context, dst, url, digest string) (string, error) {
	response, err := request(ctx, url)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	return Write(dst, response.Body, digest)
}
