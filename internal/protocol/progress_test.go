package protocol

import (
	"errors"
	"testing"
)

func TestProgressRoundTrip(t *testing.T) {
	want := Progress{Bytes: 1234, Total: 99999, Files: 2, FilesTotal: 7}
	message := EncodeProgress(want)
	if message != ".syn progress bytes=1234 total=99999 files=2 filestotal=7" {
		t.Fatalf("unexpected encoding %q", message)
	}
	got, err := ParseProgress(message + "\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseProgressIgnoresUnknownAndMissingKeys(t *testing.T) {
	got, err := ParseProgress(HiddenProgressPrefix + "future=x bytes=5  total=10")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := (Progress{Bytes: 5, Total: 10}); got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestParseProgressRejectsMalformed(t *testing.T) {
	for _, message := range []string{
		".syn probe bytes=1",
		HiddenProgressPrefix + "bytes",
		HiddenProgressPrefix + "bytes=-1",
		HiddenProgressPrefix + "total=ten",
	} {
		if _, err := ParseProgress(message); !errors.Is(err, ErrInvalidProgress) {
			t.Errorf("%q: got error %v, want ErrInvalidProgress", message, err)
		}
	}
}
