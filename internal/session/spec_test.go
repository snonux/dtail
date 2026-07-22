package session

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/mimecast/dtail/internal/omode"
)

func TestSpecStartCommandEncodesPayload(t *testing.T) {
	t.Parallel()

	spec := Spec{
		Mode:    omode.TailClient,
		Files:   []string{"/var/log/app.log"},
		Options: "plain=true",
		Regex:   "ERROR",
		Timeout: 15,
	}

	command, err := spec.StartCommand()
	if err != nil {
		t.Fatalf("StartCommand() error = %v", err)
	}
	if !strings.HasPrefix(command, "SESSION START ") {
		t.Fatalf("unexpected start command prefix: %q", command)
	}

	var decoded Spec
	if err := decodeSpecPayload(strings.TrimPrefix(command, "SESSION START "), &decoded); err != nil {
		t.Fatalf("decode start payload: %v", err)
	}
	if !reflect.DeepEqual(decoded, spec) {
		t.Fatalf("unexpected decoded spec: got %#v want %#v", decoded, spec)
	}
}

func TestSpecUpdateCommandIncludesGeneration(t *testing.T) {
	t.Parallel()

	spec := Spec{
		Mode:  omode.MapClient,
		Files: []string{"/var/log/app.log"},
		Query: "from STATS select count(*)",
	}

	command, err := spec.UpdateCommand(7)
	if err != nil {
		t.Fatalf("UpdateCommand() error = %v", err)
	}
	if !strings.HasPrefix(command, "SESSION UPDATE 7 ") {
		t.Fatalf("unexpected update command prefix: %q", command)
	}

	var decoded Spec
	if err := decodeSpecPayload(strings.TrimPrefix(command, "SESSION UPDATE 7 "), &decoded); err != nil {
		t.Fatalf("decode update payload: %v", err)
	}
	if !reflect.DeepEqual(decoded, spec) {
		t.Fatalf("unexpected decoded spec: got %#v want %#v", decoded, spec)
	}
}

func TestSpecHasJournalFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []string
		want  bool
	}{
		{
			name:  "journal file",
			files: []string{"journal:ssh.service"},
			want:  true,
		},
		{
			name:  "journal file with surrounding spaces",
			files: []string{" /var/log/app.log ", " journal:nginx.service "},
			want:  true,
		},
		{
			name:  "regular file",
			files: []string{"/var/log/app.log"},
			want:  false,
		},
		{
			name:  "journal substring is not prefix",
			files: []string{"/var/log/journal:ssh.service.log"},
			want:  false,
		},
		{
			name: "empty files",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec := Spec{Files: tc.files}
			if got := spec.HasJournalFiles(); got != tc.want {
				t.Fatalf("HasJournalFiles() = %v, want %v", got, tc.want)
			}
		})
	}
}

func decodeSpecPayload(payload string, out *Spec) error {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
