package protocol

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// HiddenProgressPrefix starts the hidden control frame in which dserver
// reports how far the read commands of a session got:
//
//	.syn progress bytes=<n> total=<n> files=<n> filestotal=<n>
//
// bytes is what the session's file reads have consumed of their files so far
// (compressed files count the compressed bytes on disk), total the summed size
// of the files it reads, files the files whose read ended and filestotal the
// files it reads. All four cover the session's current generation, i.e. the
// read commands of its plain command stream or of its latest SESSION
// START/UPDATE. Journal targets and stdin have no size and are not counted.
//
// The server sends the frame about once a second while the numbers change
// and a final one before the close handshake. It is part of protocol 5
// without a capability: a protocol 5 client of before it ignores unknown
// hidden messages, and a client talking to a server of before it simply
// gets none.
const HiddenProgressPrefix = ".syn progress "

// ErrInvalidProgress reports a progress message that does not parse.
var ErrInvalidProgress = errors.New("invalid progress message")

// Progress is the read progress of one session, as sent with
// HiddenProgressPrefix.
type Progress struct {
	// Bytes is the number of bytes of the files read so far.
	Bytes int64
	// Total is the summed size of the files to read.
	Total int64
	// Files is the number of files whose read ended.
	Files int64
	// FilesTotal is the number of files to read.
	FilesTotal int64
}

// EncodeProgress returns the hidden progress message for p.
func EncodeProgress(p Progress) string {
	return fmt.Sprintf("%sbytes=%d total=%d files=%d filestotal=%d",
		HiddenProgressPrefix, p.Bytes, p.Total, p.Files, p.FilesTotal)
}

// ParseProgress parses a hidden progress message (starting with
// HiddenProgressPrefix). Its fields are blank-separated key=value tokens:
// unknown keys are ignored and missing ones are 0, so that later servers can
// add fields. A token without '=' or a known key with a value that is no
// non-negative integer fails the message.
func ParseProgress(message string) (Progress, error) {
	rest, ok := strings.CutPrefix(strings.TrimRight(message, "\n"), HiddenProgressPrefix)
	if !ok {
		return Progress{}, fmt.Errorf("%w: missing %q prefix", ErrInvalidProgress, HiddenProgressPrefix)
	}
	var p Progress
	for _, token := range strings.Fields(rest) {
		key, value, ok := strings.Cut(token, "=")
		if !ok {
			return Progress{}, fmt.Errorf("%w: token %q", ErrInvalidProgress, token)
		}
		field := p.field(key)
		if field == nil {
			continue
		}
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n < 0 {
			return Progress{}, fmt.Errorf("%w: %s=%q", ErrInvalidProgress, key, value)
		}
		*field = n
	}
	return p, nil
}

// field returns the field of p a progress key names, or nil for an unknown
// key.
func (p *Progress) field(key string) *int64 {
	switch key {
	case "bytes":
		return &p.Bytes
	case "total":
		return &p.Total
	case "files":
		return &p.Files
	case "filestotal":
		return &p.FilesTotal
	default:
		return nil
	}
}
