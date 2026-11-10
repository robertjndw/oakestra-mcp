package activity

import (
	"encoding/json"
	"regexp"
	"strings"
)

// MaxArgsBytes caps the stored args; a pasted SLA or log blob should not
// bloat the database.
const MaxArgsBytes = 64 << 10

const redacted = "***"

var secretKeys = map[string]bool{
	"environment": true, "env": true, "cmd": true, "password": true, "secret": true,
	"token": true, "refresh_token": true, "confirmation_token": true, "authorization": true, "api_key": true,
	"apikey": true, "credentials": true, "private_key": true,
}

// Tokens can show up under arbitrary keys or inside free text fields, so
// values are matched by shape as well as by key.
var jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]*\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)

// RedactArgs marshals args, masks secrets and returns compact JSON. It never
// fails: unmarshalable input is stored as a marker so the call is still logged.
func RedactArgs(args any) string {
	if args == nil {
		return "null"
	}
	b, err := json.Marshal(args)
	if err != nil {
		return `{"unserializable":true}`
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return `{"unserializable":true}`
	}
	out, err := json.Marshal(redactValue(v))
	if err != nil {
		return `{"unserializable":true}`
	}
	if len(out) > MaxArgsBytes {
		return `{"truncated":true}`
	}
	return string(out)
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if secretKeys[strings.ToLower(k)] {
				t[k] = redacted
				continue
			}
			t[k] = redactValue(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
		return t
	case string:
		// Agents often send an SLA as a JSON string instead of an object.
		// Left as a string, its environment and cmd would slip past the key
		// matching, so it is decoded and redacted like any other object.
		if trimmed := strings.TrimSpace(t); strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
			var inner any
			if err := json.Unmarshal([]byte(trimmed), &inner); err == nil {
				return redactValue(inner)
			}
		}
		return jwtRe.ReplaceAllString(t, redacted)
	default:
		return v
	}
}
