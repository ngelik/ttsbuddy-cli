package api

import (
	"encoding/json"
	"testing"
)

func TestCuratedVoices(t *testing.T) {
	voices := CuratedVoices()
	if len(voices) != 24 {
		t.Errorf("expected 24 curated voices, got %d", len(voices))
	}
	for i, v := range voices {
		if v.ID == "" {
			t.Errorf("voice[%d] has empty ID", i)
		}
		if v.Name == "" {
			t.Errorf("voice[%d] (%s) has empty Name", i, v.ID)
		}
		if v.Gender == "" {
			t.Errorf("voice[%d] (%s) has empty Gender", i, v.ID)
		}
		if v.Language == "" {
			t.Errorf("voice[%d] (%s) has empty Language", i, v.ID)
		}
	}
	// First voice should be af_heart (default)
	if voices[0].ID != "af_heart" {
		t.Errorf("first voice should be af_heart, got %s", voices[0].ID)
	}
}

func TestParseVoiceResponseArray(t *testing.T) {
	raw := json.RawMessage(`[
		{"id": "af_heart", "name": "Heart", "gender": "Female", "language": "English"},
		{"id": "bf_emma", "name": "Emma", "gender": "Female", "language": "British English"}
	]`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(voices) != 2 {
		t.Fatalf("expected 2 voices, got %d", len(voices))
	}
	if voices[0].ID != "af_heart" {
		t.Errorf("first voice ID: got %q", voices[0].ID)
	}
	if voices[1].Name != "Emma" {
		t.Errorf("second voice Name: got %q", voices[1].Name)
	}
}

func TestParseVoiceResponseAltKeys(t *testing.T) {
	raw := json.RawMessage(`[{"voice_id": "af_heart", "name": "Heart"}]`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(voices) != 1 || voices[0].ID != "af_heart" {
		t.Errorf("expected af_heart via voice_id key, got %+v", voices)
	}
}

func TestParseVoiceResponseWrapper(t *testing.T) {
	raw := json.RawMessage(`{"voices": [{"id": "af_heart", "name": "Heart"}]}`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(voices) != 1 || voices[0].ID != "af_heart" {
		t.Errorf("expected af_heart via wrapper, got %+v", voices)
	}
}

func TestParseVoiceResponseEmpty(t *testing.T) {
	raw := json.RawMessage(`[]`)
	voices, err := parseVoiceResponse(raw)
	// Empty array: no voices but no error (valid response with no items)
	if err != nil {
		// If it errors, that's also acceptable — just lock down the behavior
		t.Logf("parseVoiceResponse([]) returns error: %v (this is acceptable)", err)
		return
	}
	if len(voices) != 0 {
		t.Errorf("expected 0 voices from empty array, got %d", len(voices))
	}
}

func TestParseVoiceResponseExtraFields(t *testing.T) {
	raw := json.RawMessage(`[{"id": "af_heart", "name": "Heart", "unknown_field": 42, "nested": {"a": 1}}]`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("should tolerate extra fields: %v", err)
	}
	if len(voices) != 1 || voices[0].ID != "af_heart" {
		t.Errorf("expected af_heart, got %+v", voices)
	}
}

func TestParseVoiceResponseBadJSON(t *testing.T) {
	raw := json.RawMessage(`not json at all`)
	_, err := parseVoiceResponse(raw)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
