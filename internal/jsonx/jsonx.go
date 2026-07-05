// Package jsonx implements the strict-JSON parsing discipline used for every
// structured model verdict (classifier, gate, pre-pass, votes, judge):
// demand JSON only in the prompt, parse defensively on the way back.
package jsonx

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Parse decodes model output into v, tolerating markdown fences and
// surrounding prose by extracting the first balanced top-level JSON object.
func Parse(raw string, v any) error {
	s := strings.TrimSpace(raw)
	if err := json.Unmarshal([]byte(s), v); err == nil {
		return nil
	}
	// Strip ```json fences.
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```json")
		s = strings.TrimPrefix(s, "```")
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
		if err := json.Unmarshal([]byte(s), v); err == nil {
			return nil
		}
	}
	// Extract the first balanced {...} block, respecting strings/escapes.
	if obj, ok := firstObject(s); ok {
		return json.Unmarshal([]byte(obj), v)
	}
	return fmt.Errorf("no JSON object found in model output")
}

// ParseLast decodes the LAST balanced JSON object in the text into v. Used
// for prose-then-verdict outputs (e.g. council rebuttal followed by a vote).
func ParseLast(raw string, v any) error {
	s := strings.TrimSpace(raw)
	last := ""
	rest := s
	for {
		obj, ok := firstObject(rest)
		if !ok {
			break
		}
		idx := strings.Index(rest, obj)
		last = obj
		rest = rest[idx+len(obj):]
	}
	if last == "" {
		return fmt.Errorf("no JSON object found in model output")
	}
	return json.Unmarshal([]byte(last), v)
}

func firstObject(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(s); i++ {
		c := s[i]
		switch {
		case escaped:
			escaped = false
		case c == '\\' && inStr:
			escaped = true
		case c == '"':
			inStr = !inStr
		case !inStr && c == '{':
			depth++
		case !inStr && c == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}
