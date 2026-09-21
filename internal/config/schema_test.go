package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// lookupRef returns the definition a local "#/definitions/<name>" reference
// points to, or an error for any other or dangling reference.
func lookupRef(root schemaNode, ref string) (schemaNode, error) {
	name, found := strings.CutPrefix(ref, "#/definitions/")
	if !found {
		return nil, fmt.Errorf("unsupported $ref %q", ref)
	}
	defs, _ := root["definitions"].(schemaNode)
	target, ok := defs[name].(schemaNode)
	if !ok {
		return nil, fmt.Errorf("dangling $ref %q", ref)
	}
	return target, nil
}

// resolve follows a local "#/definitions/<name>" reference.
func resolve(t *testing.T, root, node schemaNode) schemaNode {
	t.Helper()
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	target, err := lookupRef(root, ref)
	if err != nil {
		t.Fatal(err)
	}
	return resolve(t, root, target)
}

// supportedKeywords lists every keyword the test validator below implements,
// plus the annotations it may ignore. Any other keyword would be silently
// skipped by validate, so TestSchemaUsesOnlySupportedKeywords rejects it.
var supportedKeywords = map[string]bool{
	"$schema": true, "$ref": true, "definitions": true,
	"type": true, "enum": true, "pattern": true, "minimum": true, "maximum": true,
	"properties": true, "additionalProperties": true,
	"items": true, "additionalItems": true, "minItems": true,
	"description": true, "default": true, "deprecated": true,
}

// annotationKeywords may sit next to "$ref". Assertions may not: draft-07
// validators ignore every sibling of "$ref", 2019-09 validators apply them.
var annotationKeywords = map[string]bool{"description": true, "default": true, "deprecated": true}

var supportedTypes = map[string]bool{
	"object": true, "array": true, "string": true, "boolean": true, "integer": true,
}

// checkKeywords walks every subschema and reports keywords validate does not
// implement, unresolvable references, assertions next to "$ref", type arrays
// and patterns Go cannot compile.
func checkKeywords(root, node schemaNode, path string) []string {
	var errs []string
	fail := func(format string, args ...any) {
		errs = append(errs, path+": "+fmt.Sprintf(format, args...))
	}
	for _, key := range sortedKeys(node) {
		if !supportedKeywords[key] {
			fail("keyword %q is not supported by the test validator", key)
		}
		if key == "definitions" && path != "$" {
			fail("definitions are only supported at the root")
		}
	}
	if ref, ok := node["$ref"]; ok {
		refString, isString := ref.(string)
		if !isString {
			fail("$ref must be a string")
		} else if _, err := lookupRef(root, refString); err != nil {
			fail("%v", err)
		}
		for _, key := range sortedKeys(node) {
			if key != "$ref" && !annotationKeywords[key] {
				fail("keyword %q next to $ref", key)
			}
		}
	}
	if typ, ok := node["type"]; ok {
		if name, isString := typ.(string); !isString || !supportedTypes[name] {
			fail("unsupported type %v", typ)
		}
	}
	if pattern, ok := node["pattern"]; ok {
		if patternString, isString := pattern.(string); !isString {
			fail("pattern must be a string")
		} else if _, err := regexp.Compile(patternString); err != nil {
			fail("pattern does not compile: %v", err)
		}
	}
	subschemas := func(key string, value any) {
		child, ok := value.(schemaNode)
		if !ok {
			fail("%s must be a schema object", key)
			return
		}
		errs = append(errs, checkKeywords(root, child, path+"/"+key)...)
	}
	for _, key := range []string{"definitions", "properties"} {
		if children, ok := node[key]; ok {
			childMap, isMap := children.(schemaNode)
			if !isMap {
				fail("%s must be an object", key)
				continue
			}
			for _, name := range sortedKeys(childMap) {
				subschemas(key+"/"+name, childMap[name])
			}
		}
	}
	if extra, ok := node["additionalProperties"]; ok {
		if _, isBool := extra.(bool); !isBool {
			subschemas("additionalProperties", extra)
		}
	}
	if extra, ok := node["additionalItems"]; ok {
		if _, isBool := extra.(bool); !isBool {
			fail("additionalItems must be a boolean")
		}
	}
	switch items := node["items"].(type) {
	case nil:
	case []any:
		for i, item := range items {
			subschemas(fmt.Sprintf("items/%d", i), item)
		}
	default:
		subschemas("items", items)
	}
	return errs
}

func TestSchemaUsesOnlySupportedKeywords(t *testing.T) {
	schema := loadSchema(t)
	if errs := checkKeywords(schema, schema, "$"); len(errs) > 0 {
		t.Fatalf("schema uses constructs the test validator does not check:\n%s",
			strings.Join(errs, "\n"))
	}
}

// TestCheckKeywordsRejectsUnsupportedConstructs makes sure the keyword walk
// itself is not vacuous.
func TestCheckKeywordsRejectsUnsupportedConstructs(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr string
	}{
		{name: "unknown keyword", schema: `{"properties": {"A": {"type": "string", "maxLength": 3}}}`,
			wantErr: `keyword "maxLength"`},
		{name: "combinator", schema: `{"anyOf": [{"type": "string"}]}`, wantErr: `keyword "anyOf"`},
		{name: "type array", schema: `{"type": ["string", "null"]}`, wantErr: "unsupported type"},
		{name: "dangling ref in definitions",
			schema:  `{"definitions": {"a": {"type": "object", "additionalProperties": {"$ref": "#/definitions/b"}}}}`,
			wantErr: `dangling $ref "#/definitions/b"`},
		{name: "foreign ref", schema: `{"items": {"$ref": "other.json#/x"}}`, wantErr: "unsupported $ref"},
		{name: "assertion next to ref",
			schema:  `{"definitions": {"a": {"type": "string"}}, "properties": {"A": {"$ref": "#/definitions/a", "minimum": 1}}}`,
			wantErr: `keyword "minimum" next to $ref`},
		{name: "bad pattern", schema: `{"pattern": "("}`, wantErr: "pattern does not compile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var schema schemaNode
			if err := json.Unmarshal([]byte(tt.schema), &schema); err != nil {
				t.Fatalf("parse schema: %v", err)
			}
			errs := strings.Join(checkKeywords(schema, schema, "$"), "\n")
			if !strings.Contains(errs, tt.wantErr) {
				t.Fatalf("errors %q do not contain %q", errs, tt.wantErr)
			}
		})
	}
}

// jsonFields returns the JSON keys encoding/json decodes into typ, including the
// promoted fields of embedded structs, keyed to their struct fields.
func jsonFields(typ reflect.Type) map[string]reflect.StructField {
	fields := make(map[string]reflect.StructField)
	for _, field := range reflect.VisibleFields(typ) {
		tag := field.Tag.Get("json")
		if tag == "-" || !field.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
			continue // its fields are promoted and visited separately
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field
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

// compareType checks that the schema node describes values of typ: matching
// JSON types, recursing into struct fields, map values, slice items and the
// items of fixed-size arrays.
func compareType(t *testing.T, root, node schemaNode, typ reflect.Type, path string) {
	t.Helper()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	node = resolve(t, root, node)
	want := schemaType(typ.Kind())
	if node["type"] != want {
		t.Errorf("%s: schema type = %v, want %s", path, node["type"], want)
		return
	}
	switch typ.Kind() {
	case reflect.Struct:
		compareStruct(t, root, node, typ, path)
	case reflect.Map:
		values, ok := node["additionalProperties"].(schemaNode)
		if !ok {
			t.Errorf("%s: map schema must describe its values in additionalProperties", path)
			return
		}
		compareType(t, root, values, typ.Elem(), path+"[*]")
	case reflect.Slice:
		items, ok := node["items"].(schemaNode)
		if !ok {
			t.Errorf("%s: slice schema must describe its items with one schema", path)
			return
		}
		compareType(t, root, items, typ.Elem(), path+"[]")
	case reflect.Array:
		items, ok := node["items"].([]any)
		if !ok || len(items) != typ.Len() {
			t.Errorf("%s: array schema must list %d tuple items", path, typ.Len())
			return
		}
		if node["minItems"] != float64(typ.Len()) || node["additionalItems"] != false {
			t.Errorf("%s: array schema must require exactly %d items", path, typ.Len())
		}
		for i, item := range items {
			compareType(t, root, item.(schemaNode), typ.Elem(), fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

// compareStruct checks that an object schema lists exactly the JSON fields of
// typ, rejects unknown keys, and describes every field with compareType.
func compareStruct(t *testing.T, root, node schemaNode, typ reflect.Type, path string) {
	t.Helper()
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
		field, ok := fields[name]
		if !ok {
			t.Errorf("%s.%s: schema property has no matching config field", path, name)
			continue
		}
		fieldType := field.Type
		if fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if _, ok := props[name].(schemaNode)["description"]; !ok && fieldType.Kind() != reflect.Struct &&
			!strings.Contains(path, "TermColors") {
			t.Errorf("%s.%s: schema property lacks a description", path, name)
		}
		compareType(t, root, props[name].(schemaNode), fieldType, path+"."+name)
	}
}

// compareDefaults checks every "default" annotation against the value the
// built-in default configuration actually holds.
func compareDefaults(t *testing.T, root, node schemaNode, value reflect.Value, path string) {
	t.Helper()
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	props, _ := resolve(t, root, node)["properties"].(schemaNode)
	fields := jsonFields(value.Type())
	for _, name := range sortedKeys(props) {
		field, ok := fields[name]
		if !ok {
			continue // reported by TestSchemaMatchesConfigStructs
		}
		prop := props[name].(schemaNode)
		fieldValue := value.FieldByIndex(field.Index)
		if want, ok := prop["default"]; ok {
			raw, err := json.Marshal(fieldValue.Interface())
			if err != nil {
				t.Fatalf("%s.%s: marshal default: %v", path, name, err)
			}
			var got any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("%s.%s: unmarshal default: %v", path, name, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s.%s: schema default %v, built-in default %v", path, name, want, got)
			}
		}
		if fieldValue.Kind() == reflect.Struct ||
			(fieldValue.Kind() == reflect.Pointer && fieldValue.Elem().Kind() == reflect.Struct) {
			compareDefaults(t, root, prop, fieldValue, path+"."+name)
		}
	}
}

func TestSchemaDefaultsMatchBuiltInDefaults(t *testing.T) {
	schema := loadSchema(t)
	defaults := initializer{
		Common: newDefaultCommonConfig(),
		Server: newDefaultServerConfig(),
		Client: newDefaultClientConfig(),
	}
	compareDefaults(t, schema, schema, reflect.ValueOf(defaults), "$")
}

func TestSchemaMatchesConfigStructs(t *testing.T) {
	schema := loadSchema(t)
	// initializer is the value the config file is decoded into, so its fields
	// (the Client, Server and Common sections) form the top level.
	compareType(t, schema, schema, reflect.TypeOf(initializer{}), "$")
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
// schema uses: $ref, type, enum, pattern, minimum, maximum, properties,
// additionalProperties, items (single or tuple), additionalItems and minItems.
// TestSchemaUsesOnlySupportedKeywords guarantees the schema uses nothing else,
// and only annotations sit next to "$ref".
func validate(root, node schemaNode, value any, path string) []string {
	if ref, ok := node["$ref"].(string); ok {
		target, err := lookupRef(root, ref)
		if err != nil {
			return []string{path + ": " + err.Error()}
		}
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
		text, ok := value.(string)
		if !ok {
			fail("want string, got %T", value)
			return errs
		}
		if pattern, ok := node["pattern"].(string); ok && !regexp.MustCompile(pattern).MatchString(text) {
			fail("%q does not match the pattern %s", text, pattern)
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
		{name: "mixed-case logger names", config: `{"Common": {"Logger": "FOut", "LogLevel": "WaRn", "LogRotation": "Signal"}}`},
		{name: "empty log level", config: `{"Common": {"LogLevel": ""}}`},
		{name: "logger name", config: `{"Common": {"Logger": "syslog"}}`, wantErr: "does not match the pattern"},
		{name: "logger name prefix", config: `{"Common": {"Logger": "stdoutx"}}`, wantErr: "does not match the pattern"},
		{name: "empty logger", config: `{"Common": {"Logger": ""}}`, wantErr: "does not match the pattern"},
		{name: "log level name", config: `{"Common": {"LogLevel": "warning"}}`, wantErr: "does not match the pattern"},
		{name: "log rotation name", config: `{"Common": {"LogRotation": "weekly"}}`, wantErr: "does not match the pattern"},
		{name: "frame size limit", config: `{"Server": {"MaxCommandFrameSize": 1}}`},
		{name: "disabled frame size guard", config: `{"Server": {"MaxCommandFrameSize": 0}}`, wantErr: "below the minimum"},
		{name: "per-user permissions", config: `{"Server": {"Permissions": {"Users": {"alice": ["readfiles:^/var/log/"]}}}}`},
		{name: "per-user permission type", config: `{"Server": {"Permissions": {"Users": {"alice": [1]}}}}`, wantErr: "want string"},
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
