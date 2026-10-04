package spannerdb

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// maxUnzippedText caps what storedText inflates from bill_texts.content_gz. The pipeline stores at
// most repository.MaxCellBytes gzipped, and bill XML compresses about ten to one.
const maxUnzippedText = 256 << 20

// errTextTooLong reports a gzipped text that inflates past maxUnzippedText.
var errTextTooLong = errors.New("unzipped bill text over the size cap")

// storedText returns a bill text as downloaded (#451). content is bill_texts.content, hash its
// content_hash (the SHA-256 of the download in hex) and gz its content_gz. Content is the download
// when there is no gz, or when its hash matches; otherwise the download is gz unzipped.
func storedText(content, hash string, gz []byte) (string, error) {
	if len(gz) == 0 {
		return content, nil
	}
	sum := sha256.Sum256([]byte(content))
	if hex.EncodeToString(sum[:]) == hash {
		return content, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return "", fmt.Errorf("open gzipped bill text: %w", err)
	}
	defer func() { _ = zr.Close() }()
	var out bytes.Buffer
	n, err := io.Copy(&out, io.LimitReader(zr, maxUnzippedText+1))
	if err != nil {
		return "", fmt.Errorf("unzip bill text: %w", err)
	}
	if n > maxUnzippedText {
		return "", errTextTooLong
	}
	return out.String(), nil
}
