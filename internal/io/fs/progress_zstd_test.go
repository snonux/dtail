//go:build !nozstd

package fs

import (
	"testing"

	"github.com/DataDog/zstd"
)

func TestProgressCountsCompressedBytesOfZstd(t *testing.T) {
	compressed, err := zstd.Compress(nil, []byte(progressTestLines()))
	if err != nil {
		t.Fatal(err)
	}
	checkCounted(t, writeProgressFile(t, "log.zst", compressed), 20000)
}
