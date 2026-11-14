package result

import (
	"encoding/json"
	"testing"
)

// A Runner's detail that isn't JSON can't be stored or served, so Validate
// refuses it; an empty detail and any valid JSON stay fine (R16c).
func TestRx_ValidateChecksDetailIsJSON(t *testing.T) {
	for _, c := range []struct {
		name   string
		detail json.RawMessage
		ok     bool
	}{
		{"not json", json.RawMessage("{not json"), false},
		{"truncated object", json.RawMessage(`{"idle":[[0.05,8.1]`), false},
		{"nil", nil, true},
		{"empty", json.RawMessage{}, true},
		{"null", json.RawMessage("null"), true},
		{"empty object", json.RawMessage("{}"), true},
		{"object", json.RawMessage(`{"idle":[[0.05,8.1]],"rate":[[1.2,870.4,0]]}`), true},
		{"array", json.RawMessage("[1]"), true},
		{"string", json.RawMessage(`"x"`), true},
		{"number", json.RawMessage("1"), true},
	} {
		s := Submission{DownloadMbps: 100, Detail: c.detail}
		err := s.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: Validate = %v, want nil", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: Validate = nil, want an error", c.name)
		}
	}
}

// Bytes that aren't UTF-8 inside a JSON string pass json.Valid but make the
// stored result serve as invalid JSON, so Validate refuses them. \u escapes are
// ASCII, a lone surrogate included, and stay valid (R16c, B30).
func TestRx_ValidateChecksDetailIsUTF8(t *testing.T) {
	for _, c := range []struct {
		name   string
		detail json.RawMessage
		ok     bool
	}{
		{"invalid bytes in a string", json.RawMessage("{\"x\":\"a\xff\xfeb\"}"), false},
		{"a truncated rune in a string", json.RawMessage("{\"x\":\"\xe4\xb8\"}"), false},
		{"a lone surrogate escape", json.RawMessage(`{"x":"\udc00"}`), true},
		{"a rune escape", json.RawMessage(`{"x":"世"}`), true},
		{"a UTF-8 string", json.RawMessage(`{"x":"世界"}`), true},
	} {
		s := Submission{DownloadMbps: 100, Detail: c.detail}
		err := s.Validate()
		if c.ok && err != nil {
			t.Errorf("%s: Validate = %v, want nil", c.name, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s: Validate = nil, want an error", c.name)
		}
	}
}
