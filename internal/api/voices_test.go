package api

import (
	"encoding/json"
	"testing"
)

func TestCuratedVoices(t *testing.T) {
	voices := CuratedVoices()
	if len(voices) < 300 {
		t.Errorf("expected curated voices to include Supertonic language modes, got %d", len(voices))
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

	foundFrenchM1 := false
	foundKoreanF1 := false
	for _, v := range voices {
		if v.ID == "st_m1" && v.Name == "Louis" && v.Language == "French" && v.LanguageCode == "fr" && v.Quality == "Fast" {
			foundFrenchM1 = true
		}
		if v.ID == "st_f1" && v.Name == "서연" && v.Language == "Korean" && v.LanguageCode == "ko" && v.Quality == "Fast" {
			foundKoreanF1 = true
		}
	}
	if !foundFrenchM1 {
		t.Error("curated voices should include st_m1 French Fast mode named Louis")
	}
	if !foundKoreanF1 {
		t.Error("curated voices should include st_f1 Korean Fast mode named 서연")
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

func TestParseVoiceResponseCodeKey(t *testing.T) {
	// The live API uses "code" as the voice ID key
	raw := json.RawMessage(`[{"code": "af_heart", "name": "Madison", "gender": "female", "language": "American English"}]`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(voices) != 1 || voices[0].ID != "af_heart" {
		t.Errorf("expected af_heart via code key, got %+v", voices)
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

func TestParseVoiceResponseEmptyArray(t *testing.T) {
	raw := json.RawMessage(`[]`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("empty array should be valid: %v", err)
	}
	if len(voices) != 0 {
		t.Errorf("expected 0 voices from empty array, got %d", len(voices))
	}
}

func TestParseVoiceResponseJunkArray(t *testing.T) {
	// Non-empty array with no valid voice IDs should error
	raw := json.RawMessage(`[{"x": 1}, {"y": "hello"}]`)
	_, err := parseVoiceResponse(raw)
	if err == nil {
		t.Error("junk non-empty array should return error")
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

func TestParseVoiceResponseExpandsSupertonicLanguageModes(t *testing.T) {
	raw := json.RawMessage(`{
		"voices": [
			{
				"code": "ff_siwis",
				"name": "Camille",
				"gender": "female",
				"language": "French",
				"language_code": "f"
			},
			{
				"code": "st_m1",
				"name": "M1",
				"gender": "male",
				"language": "Multilingual",
				"language_code": "en",
				"engine": "supertonic",
				"supported_language_codes": ["en", "fr", "de", "na"]
			}
		]
	}`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertVoiceMode := func(id, name, language, languageCode string) {
		t.Helper()
		for _, voice := range voices {
			if voice.ID == id && voice.Name == name && voice.Language == language && voice.LanguageCode == languageCode {
				return
			}
		}
		t.Fatalf("missing voice mode id=%s name=%s language=%s languageCode=%s in %+v", id, name, language, languageCode, voices)
	}

	assertVoiceMode("ff_siwis", "Camille", "French", "f")
	assertVoiceMode("st_m1", "Liam", "American English", "en")
	assertVoiceMode("st_m1", "Louis", "French", "fr")
	assertVoiceMode("st_m1", "Noah", "German", "de")

	for _, voice := range voices {
		if voice.LanguageCode == "na" || voice.Language == "Multilingual" {
			t.Fatalf("voice response should not expose %q/%q", voice.Language, voice.LanguageCode)
		}
	}
}

func TestParseVoiceResponseCanonicalizesSupertonicAliases(t *testing.T) {
	raw := json.RawMessage(`{
		"voices": [
			{
				"voice_id": "st_f1",
				"code": "F1",
				"name": "F1",
				"gender": "female",
				"language": "Multilingual",
				"language_code": "en",
				"engine": "supertonic",
				"supported_language_codes": ["en", "fr"]
			},
			{
				"voice_id": "st_m1",
				"code": "M1",
				"name": "M1",
				"gender": "male",
				"language": "Multilingual",
				"language_code": "en",
				"engine": "supertonic",
				"supported_language_codes": ["en", "fr"]
			}
		]
	}`)
	voices, err := parseVoiceResponse(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertVoiceMode := func(id, name, language, languageCode string) {
		t.Helper()
		for _, voice := range voices {
			if voice.ID == id && voice.Name == name && voice.Language == language && voice.LanguageCode == languageCode {
				return
			}
		}
		t.Fatalf("missing voice mode id=%s name=%s language=%s languageCode=%s in %+v", id, name, language, languageCode, voices)
	}

	assertVoiceMode("st_f1", "Ava", "American English", "en")
	assertVoiceMode("st_m1", "Liam", "American English", "en")
	assertVoiceMode("st_m1", "Louis", "French", "fr")

	for _, voice := range voices {
		if voice.ID == "F1" || voice.ID == "M1" || voice.Name == "F1" || voice.Name == "M1" {
			t.Fatalf("Supertonic aliases should be canonicalized before display: %+v", voice)
		}
	}
}

func TestParseVoiceResponseBadJSON(t *testing.T) {
	raw := json.RawMessage(`not json at all`)
	_, err := parseVoiceResponse(raw)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
