package repository

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxJSONBytes = 16 << 20

// Canonical encodes a JSON value with deterministic key ordering, no
// insignificant whitespace and raw UTF-8, so that its bytes can be hashed.
func Canonical(value any) ([]byte, error) {
	var buffer bytes.Buffer
	if err := writeCanonical(&buffer, value); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Hash returns the lowercase SHA-256 of the canonical JSON encoding.
func Hash(value any) (string, error) {
	data, err := Canonical(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonical(buffer *bytes.Buffer, value any) error {
	switch typed := value.(type) {
	case nil:
		buffer.WriteString("null")
	case bool:
		if typed {
			buffer.WriteString("true")
		} else {
			buffer.WriteString("false")
		}
	case string:
		if !utf8.ValidString(typed) {
			return errors.New("strings must contain valid UTF-8")
		}
		writeJSONString(buffer, typed)
	case json.Number:
		if err := validNumber(string(typed)); err != nil {
			return err
		}
		buffer.WriteString(string(typed))
	case int:
		buffer.WriteString(strconv.Itoa(typed))
	case int64:
		buffer.WriteString(strconv.FormatInt(typed, 10))
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return errors.New("non-finite numbers are not valid JSON values")
		}
		buffer.WriteString(strconv.FormatFloat(typed, 'g', -1, 64))
	case []any:
		// A typed nil slice or map must canonicalize as null, matching
		// encoding/json: wire bytes produced by json.Marshal and values decoded
		// from them must hash identically to their Go-side projections.
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		buffer.WriteByte('[')
		for index, item := range typed {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonical(buffer, item); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
	case []string:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, item)
		}
		return writeCanonical(buffer, items)
	case map[string]any:
		if typed == nil {
			buffer.WriteString("null")
			return nil
		}
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeJSONString(buffer, key)
			buffer.WriteByte(':')
			if err := writeCanonical(buffer, typed[key]); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
	default:
		return writeReflected(buffer, value)
	}
	return nil
}

func writeReflected(buffer *bytes.Buffer, value any) error {
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Slice, reflect.Array:
		if reflected.Kind() != reflect.Array && reflected.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		buffer.WriteByte('[')
		for index := 0; index < reflected.Len(); index++ {
			if index > 0 {
				buffer.WriteByte(',')
			}
			if err := writeCanonical(buffer, reflected.Index(index).Interface()); err != nil {
				return err
			}
		}
		buffer.WriteByte(']')
		return nil
	case reflect.Map:
		if reflected.IsNil() {
			buffer.WriteString("null")
			return nil
		}
		keys := make([]string, 0, reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			key, ok := iterator.Key().Interface().(string)
			if !ok {
				return errors.New("canonical object keys must be strings")
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buffer.WriteByte('{')
		for index, key := range keys {
			if index > 0 {
				buffer.WriteByte(',')
			}
			writeJSONString(buffer, key)
			buffer.WriteByte(':')
			if err := writeCanonical(buffer, reflected.MapIndex(reflect.ValueOf(key)).Interface()); err != nil {
				return err
			}
		}
		buffer.WriteByte('}')
		return nil
	default:
		return fmt.Errorf("unsupported canonical value type %T", value)
	}
}

func writeJSONString(buffer *bytes.Buffer, value string) {
	buffer.WriteByte('"')
	for index := 0; index < len(value); {
		character := value[index]
		if character < 0x20 || character == '"' || character == '\\' {
			switch character {
			case '\b':
				buffer.WriteString(`\b`)
			case '\t':
				buffer.WriteString(`\t`)
			case '\n':
				buffer.WriteString(`\n`)
			case '\f':
				buffer.WriteString(`\f`)
			case '\r':
				buffer.WriteString(`\r`)
			case '"':
				buffer.WriteString(`\"`)
			case '\\':
				buffer.WriteString(`\\`)
			default:
				fmt.Fprintf(buffer, `\u%04x`, character)
			}
			index++
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		if r == utf8.RuneError && size == 1 {
			return
		}
		buffer.WriteString(value[index : index+size])
		index += size
	}
	buffer.WriteByte('"')
}

func validNumber(text string) error {
	if !jsonNumberPattern.MatchString(text) {
		return fmt.Errorf("invalid JSON number %q", text)
	}
	return nil
}

var jsonNumberPattern = regexp.MustCompile(`^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$`)

// DecodeObject parses a top-level JSON object while preserving number literals.
func DecodeObject(data []byte) (map[string]any, error) {
	if len(data) > maxJSONBytes {
		return nil, errors.New("JSON input exceeds the maximum allowed size")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("JSON input must be valid UTF-8")
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("unexpected data after JSON value")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("top-level JSON value must be an object")
	}
	return object, nil
}

func asString(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func asBool(value any) (bool, bool) {
	flag, ok := value.(bool)
	return flag, ok
}

func asInt(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return number, true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	}
	return 0, false
}

func asStringSlice(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func toAnySlice(values []string) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}
