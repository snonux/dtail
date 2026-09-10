//go:build !nozstd

package fs

import (
	"bufio"
	"io"
	"os"

	"github.com/DataDog/zstd"
)

func (f *readFile) makeZstdReader(fd *os.File) (reader *bufio.Reader, decompressor io.Closer, err error) {
	f.logger.Info(f.FilePath(), "Detected zstd compression format")
	zstdReader := zstd.NewReader(fd)
	decompressor = zstdReader
	reader = bufio.NewReader(zstdReader)
	return
}
