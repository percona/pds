package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// Clients built before an environment was called one still say cluster: the
// agents already joined, and published releases of the MCP server. For one
// release the router accepts their routes, query parameters and body fields,
// and every JSON response carries the old field beside the new one. Removing
// this file ends the window.
const (
	legacyAPIPrefix         = "/api/clusters"
	legacyInternalPrefix    = "/api/internal/clusters"
	currentAPIPrefix        = "/api/environments"
	currentInternalPrefix   = "/api/internal/environments"
	legacyEnvironmentField  = "cluster"
	legacyEnvironmentsField = "clusters"
)

// LegacyNames rewrites a request that uses the old names into the current
// ones before the router sees it.
func LegacyNames(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = legacyPath(r.URL.Path)

		if query := r.URL.Query(); query.Has(legacyEnvironmentField) && !query.Has("environment") {
			query.Set("environment", query.Get(legacyEnvironmentField))
			query.Del(legacyEnvironmentField)
			r.URL.RawQuery = query.Encode()
		}

		if r.Body != nil && r.Body != http.NoBody {
			r.Body = legacyBody(r.Body)
		}

		next.ServeHTTP(w, r)
	})
}

func legacyPath(path string) string {
	for _, p := range [][2]string{
		{legacyInternalPrefix, currentInternalPrefix},
		{legacyAPIPrefix, currentAPIPrefix},
	} {
		if path == p[0] || strings.HasPrefix(path, p[0]+"/") {
			return p[1] + strings.TrimPrefix(path, p[0])
		}
	}

	return path
}

// legacyBody renames a top level cluster key in a JSON object to environment.
// A body over the request cap is passed on untouched, so the handler's own
// limit still answers it.
func legacyBody(body io.ReadCloser) io.ReadCloser {
	read, err := io.ReadAll(io.LimitReader(body, int64(maxRequestBody)+1))
	rest := io.MultiReader(bytes.NewReader(read), body)
	if err != nil || len(read) > maxRequestBody || !bytes.Contains(read, []byte(`"`+legacyEnvironmentField+`"`)) {
		return readCloser{Reader: rest, Closer: body}
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(read, &fields) != nil {
		return readCloser{Reader: rest, Closer: body}
	}
	legacy, ok := fields[legacyEnvironmentField]
	if _, current := fields["environment"]; !ok || current {
		return readCloser{Reader: rest, Closer: body}
	}
	delete(fields, legacyEnvironmentField)
	fields["environment"] = legacy
	renamed, err := json.Marshal(fields)
	if err != nil {
		return readCloser{Reader: rest, Closer: body}
	}

	return readCloser{Reader: bytes.NewReader(renamed), Closer: body}
}

type readCloser struct {
	io.Reader
	io.Closer
}

// withLegacyNames adds cluster beside every environment and clusters beside
// every environments in a JSON document, so a client that predates the rename
// keeps reading the fields it knows. Field order and the bytes of every other
// value stay as they were. A document with nothing to add comes back as is.
func withLegacyNames(body []byte) []byte {
	if !bytes.Contains(body, []byte(`"environment`)) {
		return body
	}

	return addLegacyNames(body)
}

func addLegacyNames(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return raw
	}
	switch trimmed[0] {
	case '{':
		return addLegacyNamesToObject(trimmed)
	case '[':
		return addLegacyNamesToArray(trimmed)
	default:
		return raw
	}
}

func addLegacyNamesToArray(raw json.RawMessage) json.RawMessage {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return raw
	}
	var out bytes.Buffer
	out.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			out.WriteByte(',')
		}
		out.Write(addLegacyNames(item))
	}
	out.WriteByte(']')

	return out.Bytes()
}

func addLegacyNamesToObject(raw json.RawMessage) json.RawMessage {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if _, err := decoder.Token(); err != nil {
		return raw
	}
	type field struct {
		key   string
		value json.RawMessage
	}
	var fields []field
	seen := map[string]json.RawMessage{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, isString := keyToken.(string)
		if err != nil || !isString {
			return raw
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return raw
		}
		value = addLegacyNames(value)
		fields = append(fields, field{key, value})
		seen[key] = value
	}
	for _, pair := range [][2]string{
		{"environment", legacyEnvironmentField},
		{"environments", legacyEnvironmentsField},
	} {
		if value, ok := seen[pair[0]]; ok {
			if _, taken := seen[pair[1]]; !taken {
				fields = append(fields, field{pair[1], value})
			}
		}
	}

	var out bytes.Buffer
	out.WriteByte('{')
	for i, f := range fields {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(f.key)
		out.Write(key)
		out.WriteByte(':')
		out.Write(f.value)
	}
	out.WriteByte('}')

	return out.Bytes()
}
