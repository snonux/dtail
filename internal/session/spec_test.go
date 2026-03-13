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

func decodeSpecPayload(payload string, out *Spec) error {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}
