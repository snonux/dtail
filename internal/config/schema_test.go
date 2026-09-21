package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The JSON schema shipped in examples/ must describe exactly the keys the
// lenient config decoder understands. These tests keep the two in sync so a new
// config field cannot silently be rejected by schema validation again.

const schemaPath = "../../examples/dtail.schema.json"

type schemaNode = map[string]any

func loadSchema(t *testing.T) schemaNode {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var schema schemaNode
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return schema
}

// resolve follows a local "#/definitions/<name>" reference.
func resolve(t *testing.T, root, node schemaNode) schemaNode {
	t.Helper()
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	name, found := strings.CutPrefix(ref, "#/definitions/")
	if !found {
		t.Fatalf("unsupported $ref %q", ref)
	}
	defs, _ := root["definitions"].(schemaNode)
	target, ok := defs[name].(schemaNode)
	if !ok {
		t.Fatalf("dangling $ref %q", ref)
	}
	return resolve(t, root, target)
}

// jsonFields returns the JSON keys encoding/json decodes into typ, including the
// promoted fields of embedded structs.
func jsonFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
			for key, fieldType := range jsonFields(field.Type) {
				fields[key] = fieldType
			}
			continue
		}
		if !field.IsExported() {
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

func schemaType(kind reflect.Kind) string {
	switch kind {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Slice, reflect.Array:
		return "array"
	case reflect.Struct, reflect.Map:
		return "object"
	default:
		return "unsupported:" + kind.String()
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// compareStruct checks that an object schema lists exactly the JSON fields of
// typ, rejects unknown keys, and uses matching JSON types, recursing into
// nested structs and slices of structs.
func compareStruct(t *testing.T, root, node schemaNode, typ reflect.Type, path string) {
	t.Helper()
	node = resolve(t, root, node)
	if node["type"] != "object" {
		t.Errorf("%s: schema type = %v, want object", path, node["type"])
	}
	if node["additionalProperties"] != false {
		t.Errorf("%s: schema must set additionalProperties to false", path)
	}
	props, _ := node["properties"].(schemaNode)
	fields := jsonFields(typ)
	for _, name := range sortedKeys(fields) {
		if _, ok := props[name]; !ok {
			t.Errorf("%s.%s: config field is missing from %s", path, name, schemaPath)
		}
	}
	for _, name := range sortedKeys(props) {
		fieldType, ok := fields[name]
		if !ok {
			t.Errorf("%s.%s: schema property has no matching config field", path, name)
			continue
		}
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		prop := resolve(t, root, props[name].(schemaNode))
		want := schemaType(fieldType.Kind())
		if prop["type"] != want {
			t.Errorf("%s.%s: schema type = %v, want %s", path, name, prop["type"], want)
		}
		if _, ok := props[name].(schemaNode)["description"]; !ok && want != "object" &&
			!strings.Contains(path, "TermColors") {
			t.Errorf("%s.%s: schema property lacks a description", path, name)
		}
		switch fieldType.Kind() {
		case reflect.Struct:
			compareStruct(t, root, prop, fieldType, path+"."+name)
		case reflect.Slice:
			if fieldType.Elem().Kind() == reflect.Struct {
				items, _ := prop["items"].(schemaNode)
				compareStruct(t, root, items, fieldType.Elem(), path+"."+name+"[]")
			}
		}
	}
}

func TestSchemaMatchesConfigStructs(t *testing.T) {
	schema := loadSchema(t)
	// initializer is the value the config file is decoded into, so its fields
	// (the Client, Server and Common sections) form the top level.
	compareStruct(t, schema, schema, reflect.TypeOf(initializer{}), "$")
}

func TestSchemaHasNoMisspelledReferences(t *testing.T) {
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if bytes.Contains(raw, []byte(`"#ref"`)) {
		t.Fatal(`schema contains "#ref"; use "$ref" so the reference is enforced`)
	}
}

// validate is a minimal JSON-schema validator covering the keywords the dtail
// schema uses: $ref, type, enum, minimum, maximum, properties,
// additionalProperties, items (single or tuple), additionalItems and minItems.
func validate(root, node schemaNode, value any, path string) []string {
	if ref, ok := node["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/definitions/")
		target, _ := root["definitions"].(schemaNode)[name].(schemaNode)
		return validate(root, target, value, path)
	}
	var errs []string
	fail := func(format string, args ...any) {
		errs = append(errs, path+": "+fmt.Sprintf(format, args...))
	}
	switch node["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			fail("want object, got %T", value)
			return errs
		}
		props, _ := node["properties"].(schemaNode)
		for _, key := range sortedKeys(object) {
			if propNode, ok := props[key].(schemaNode); ok {
				errs = append(errs, validate(root, propNode, object[key], path+"."+key)...)
				continue
			}
			switch extra := node["additionalProperties"].(type) {
			case bool:
				if !extra {
					fail("unknown key %q", key)
				}
			case schemaNode:
				errs = append(errs, validate(root, extra, object[key], path+"."+key)...)
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			fail("want array, got %T", value)
			return errs
		}
		if minItems, ok := node["minItems"].(float64); ok && float64(len(array)) < minItems {
			fail("want at least %v items, got %d", minItems, len(array))
		}
		switch items := node["items"].(type) {
		case schemaNode:
			for i, item := range array {
				errs = append(errs, validate(root, items, item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		case []any:
			for i, item := range array {
				if i >= len(items) {
					if node["additionalItems"] == false {
						fail("too many items")
					}
					break
				}
				errs = append(errs, validate(root, items[i].(schemaNode), item, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			fail("want string, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			fail("want boolean, got %T", value)
		}
	case "integer":
		number, ok := value.(float64)
		if !ok || number != float64(int64(number)) {
			fail("want integer, got %v", value)
			return errs
		}
		if minimum, ok := node["minimum"].(float64); ok && number < minimum {
			fail("%v is below the minimum %v", number, minimum)
		}
		if maximum, ok := node["maximum"].(float64); ok && number > maximum {
			fail("%v is above the maximum %v", number, maximum)
		}
	}
	if enum, ok := node["enum"].([]any); ok {
		found := false
		for _, allowed := range enum {
			if allowed == value {
				found = true
				break
			}
		}
		if !found {
			fail("%v is not one of %v", value, enum)
		}
	}
	return errs
}

func validateConfigText(t *testing.T, schema schemaNode, text string) []string {
	t.Helper()
	var document any
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return validate(schema, schema, document, "$")
}

func TestSchemaAcceptsShippedConfigs(t *testing.T) {
	schema := loadSchema(t)
	paths := []string{"../../examples/dtail.json.example", "../../docker/dtail.json"}
	integrationConfigs, err := filepath.Glob("../../integrationtests/*.json")
	if err != nil {
		t.Fatalf("glob integration configs: %v", err)
	}
	paths = append(paths, integrationConfigs...)

	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if errs := validateConfigText(t, schema, string(raw)); len(errs) > 0 {
			t.Errorf("%s does not validate:\n%s", path, strings.Join(errs, "\n"))
		}
		// The shipped configs must also consist only of keys dtail decodes.
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&initializer{}); err != nil {
			t.Errorf("%s contains a key dtail does not decode: %v", path, err)
		}
	}
}

// TestSchemaAcceptsEveryDefaultField marshals the complete default
// configuration, which sets every field, and validates it against the schema.
func TestSchemaAcceptsEveryDefaultField(t *testing.T) {
	schema := loadSchema(t)
	defaults := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	defaults.Server.HostKeyFile = "cache/ssh_host_key"
	defaults.Server.HostKeyPath = "cache/ssh_host_key"
	defaults.Server.AuthorizedKeysPath = "/etc/dserver/authorized_keys"
	defaults.Server.KeyExchanges = []string{"curve25519-sha256"}
	defaults.Server.Ciphers = []string{"aes256-gcm@openssh.com"}
	defaults.Server.MACs = []string{"hmac-sha2-256-etm@openssh.com"}
	defaults.Server.Permissions.Users = map[string][]string{"alice": {"readfiles:^/var/log/.*"}}
	common := jobCommons{Name: "job", Enable: true, Files: "/var/log/*.log",
		Query: "from STATS select count($line) group by $hostname", Outfile: "out.$today.csv",
		Discovery: "file", Servers: []string{"server1"}, AllowFrom: []string{"server1"}}
	defaults.Server.Schedule = []Scheduled{{jobCommons: common, TimeRange: [2]int{0, 24}}}
	defaults.Server.Continuous = []Continuous{{jobCommons: common, RestartOnDayChange: true}}
	defaults.Common.HostnameOverride = "host"
	defaults.Common.ExperimentalFeaturesEnable = true
	defaults.Client.KnownHostsPath = "known_hosts"
	defaults.Client.AuthKeyPath = "id_rsa"
	defaults.Client.AuthKeyDisable = true
	defaults.Client.LogPayload = true

	raw, err := json.Marshal(defaults)
	if err != nil {
		t.Fatalf("marshal defaults: %v", err)
	}
	// The in-memory palette holds escape sequences, not the names a config
	// file uses, so validate the palette separately through the example file.
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("unmarshal defaults: %v", err)
	}
	delete(document["Client"].(map[string]any), "TermColors")
	if errs := validate(schema, schema, document, "$"); len(errs) > 0 {
		t.Fatalf("default configuration does not validate:\n%s", strings.Join(errs, "\n"))
	}
}

func TestSchemaValidation(t *testing.T) {
	schema := loadSchema(t)
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{name: "idle session timeout", config: `{"Server": {"IdleSessionTimeoutS": 900}}`},
		{name: "idle session timeout default sentinel", config: `{"Server": {"IdleSessionTimeoutS": 0}}`},
		{name: "lower-case logger names", config: `{"Common": {"Logger": "fout", "LogLevel": "info", "LogRotation": "daily"}}`},
		{name: "removed turbo key", config: `{"Server": {"TurboBoostDisable": true}}`, wantErr: `unknown key "TurboBoostDisable"`},
		{name: "unknown top-level section", config: `{"Servers": {}}`, wantErr: `unknown key "Servers"`},
		{name: "ssh port range", config: `{"Common": {"SSHPort": 70000}}`, wantErr: "above the maximum"},
		{name: "logger enum", config: `{"Common": {"Logger": "syslog"}}`, wantErr: "is not one of"},
		{name: "color enum", config: `{"Client": {"TermColors": {"Server": {"TextFg": "Purple"}}}}`, wantErr: "is not one of"},
		{name: "time range length", config: `{"Server": {"Schedule": [{"TimeRange": [1]}]}}`, wantErr: "at least 2 items"},
		{name: "wrong type", config: `{"Server": {"IdleSessionTimeoutS": "900"}}`, wantErr: "want integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := strings.Join(validateConfigText(t, schema, tt.config), "\n")
			if tt.wantErr == "" && errs != "" {
				t.Fatalf("unexpected validation errors:\n%s", errs)
			}
			if tt.wantErr != "" && !strings.Contains(errs, tt.wantErr) {
				t.Fatalf("validation errors %q do not contain %q", errs, tt.wantErr)
			}
		})
	}
}
