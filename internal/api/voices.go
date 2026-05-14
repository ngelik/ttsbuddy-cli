package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Voice represents a TTS voice.
type Voice struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Gender       string `json:"gender"`
	Language     string `json:"language"`
	LanguageCode string `json:"language_code,omitempty"`
	Engine       string `json:"engine,omitempty"`
	Quality      string `json:"quality,omitempty"`
	IsPremium    bool   `json:"is_premium,omitempty"`
}

const supertonicEngine = "supertonic"

var supertonicLanguageNames = map[string]string{
	"ar": "Arabic",
	"bg": "Bulgarian",
	"cs": "Czech",
	"da": "Danish",
	"de": "German",
	"el": "Greek",
	"en": "American English",
	"es": "Spanish",
	"et": "Estonian",
	"fi": "Finnish",
	"fr": "French",
	"hi": "Hindi",
	"hr": "Croatian",
	"hu": "Hungarian",
	"id": "Indonesian",
	"it": "Italian",
	"ja": "Japanese",
	"ko": "Korean",
	"lt": "Lithuanian",
	"lv": "Latvian",
	"nl": "Dutch",
	"pl": "Polish",
	"pt": "Portuguese",
	"ro": "Romanian",
	"ru": "Russian",
	"sk": "Slovak",
	"sl": "Slovenian",
	"sv": "Swedish",
	"tr": "Turkish",
	"uk": "Ukrainian",
	"vi": "Vietnamese",
}

var supertonicLanguageCodes = []string{
	"en", "fr", "de", "ja", "ko", "ar", "es", "it", "pt", "hi",
	"nl", "pl", "ru", "tr", "sv", "uk", "vi", "id", "cs", "da",
	"el", "fi", "hu", "ro", "sk", "bg", "hr", "lt", "lv", "sl", "et",
}

var kokoroLanguageNames = map[string]string{
	"a": "American English",
	"b": "British English",
	"e": "Spanish",
	"f": "French",
	"h": "Hindi",
	"i": "Italian",
	"j": "Japanese",
	"p": "Portuguese",
	"z": "Chinese",
}

var hiddenSupertonicLanguageCodes = map[string]bool{"na": true}

// CuratedVoices returns the offline-friendly curated voice list.
// Voice codes verified against the live API at /api/v1/voices.
func CuratedVoices() []Voice {
	voices := []Voice{
		// American English
		{ID: "af_heart", Name: "Madison", Gender: "Female", Language: "American English", LanguageCode: "a"},
		{ID: "af_bella", Name: "Sophia", Gender: "Female", Language: "American English", LanguageCode: "a"},
		{ID: "af_nicole", Name: "Zoe", Gender: "Female", Language: "American English", LanguageCode: "a"},
		{ID: "af_sarah", Name: "Harper", Gender: "Female", Language: "American English", LanguageCode: "a"},
		{ID: "af_alloy", Name: "Allie", Gender: "Female", Language: "American English", LanguageCode: "a"},
		{ID: "am_michael", Name: "Michael", Gender: "Male", Language: "American English", LanguageCode: "a"},
		{ID: "am_adam", Name: "Jackson", Gender: "Male", Language: "American English", LanguageCode: "a"},
		{ID: "am_echo", Name: "Nathan", Gender: "Male", Language: "American English", LanguageCode: "a"},
		// British English
		{ID: "bf_emma", Name: "Emma", Gender: "Female", Language: "British English", LanguageCode: "b"},
		{ID: "bm_george", Name: "George", Gender: "Male", Language: "British English", LanguageCode: "b"},
		// Spanish
		{ID: "ef_dora", Name: "Valentina", Gender: "Female", Language: "Spanish", LanguageCode: "e"},
		{ID: "em_alex", Name: "Alejandro", Gender: "Male", Language: "Spanish", LanguageCode: "e"},
		// French
		{ID: "ff_siwis", Name: "Camille", Gender: "Female", Language: "French", LanguageCode: "f"},
		// Hindi
		{ID: "hf_alpha", Name: "Priya", Gender: "Female", Language: "Hindi", LanguageCode: "h"},
		{ID: "hm_omega", Name: "Arjun", Gender: "Male", Language: "Hindi", LanguageCode: "h"},
		// Italian
		{ID: "if_sara", Name: "Giulia", Gender: "Female", Language: "Italian", LanguageCode: "i"},
		{ID: "im_nicola", Name: "Marco", Gender: "Male", Language: "Italian", LanguageCode: "i"},
		// Japanese
		{ID: "jf_alpha", Name: "Yuki", Gender: "Female", Language: "Japanese", LanguageCode: "j"},
		{ID: "jm_kumo", Name: "Hiroshi", Gender: "Male", Language: "Japanese", LanguageCode: "j"},
		// Portuguese
		{ID: "pf_dora", Name: "Isabela", Gender: "Female", Language: "Portuguese", LanguageCode: "p"},
		{ID: "pm_alex", Name: "Rafael", Gender: "Male", Language: "Portuguese", LanguageCode: "p"},
		// Chinese
		{ID: "zf_xiaobei", Name: "Mei", Gender: "Female", Language: "Chinese", LanguageCode: "z"},
		{ID: "zm_yunjian", Name: "Wei", Gender: "Male", Language: "Chinese", LanguageCode: "z"},
	}

	for _, voice := range []Voice{
		{ID: "st_m1", Name: "M1", Gender: "Male", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_m2", Name: "M2", Gender: "Male", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_m3", Name: "M3", Gender: "Male", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_m4", Name: "M4", Gender: "Male", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_m5", Name: "M5", Gender: "Male", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_f1", Name: "F1", Gender: "Female", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_f2", Name: "F2", Gender: "Female", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_f3", Name: "F3", Gender: "Female", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_f4", Name: "F4", Gender: "Female", Engine: supertonicEngine, Quality: "Fast"},
		{ID: "st_f5", Name: "F5", Gender: "Female", Engine: supertonicEngine, Quality: "Fast"},
	} {
		voices = append(voices, expandVoiceModes(voice, supertonicLanguageCodes)...)
	}

	return voices
}

// parseVoiceResponse parses the upstream TTS API voice response.
// Tolerant to schema expansion — maps only the fields we need.
func parseVoiceResponse(raw json.RawMessage) ([]Voice, error) {
	// The upstream API may return various structures. Try common shapes.

	// Try array of objects with voice_id / name fields
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err == nil {
		// Empty array is a valid (if unusual) response
		if len(arr) == 0 {
			return []Voice{}, nil
		}
		var voices []Voice
		for _, item := range arr {
			v := Voice{}
			if code, ok := item["code"].(string); ok {
				v.ID = code
			} else if id, ok := item["voice_id"].(string); ok {
				v.ID = id
			} else if id, ok := item["id"].(string); ok {
				v.ID = id
			}
			if name, ok := item["name"].(string); ok {
				v.Name = name
			}
			if gender, ok := item["gender"].(string); ok {
				v.Gender = gender
			}
			if lang, ok := item["language"].(string); ok {
				v.Language = lang
			}
			if langCode, ok := item["language_code"].(string); ok {
				v.LanguageCode = strings.ToLower(langCode)
			}
			if engine, ok := item["engine"].(string); ok {
				v.Engine = engine
			}
			if grade, ok := item["quality_grade"].(string); ok {
				v.Quality = grade
			}
			if premium, ok := item["is_premium"].(bool); ok {
				v.IsPremium = premium
			}
			if v.ID != "" {
				voices = append(voices, expandVoiceModes(v, stringSlice(item["supported_language_codes"]))...)
			}
		}
		if len(voices) > 0 {
			return voices, nil
		}
		// Non-empty array but no valid voice IDs → error
		return nil, fmt.Errorf("voice response contained %d items but none had a valid ID", len(arr))
	}

	// Try object with voices array
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err == nil {
		if voicesRaw, ok := obj["voices"]; ok {
			return parseVoiceResponse(voicesRaw)
		}
	}

	return nil, fmt.Errorf("unexpected voice response format")
}

func expandVoiceModes(voice Voice, supportedLanguageCodes []string) []Voice {
	if isSupertonicVoice(voice) {
		codes := supportedLanguageCodes
		if len(codes) == 0 {
			codes = []string{orString(voice.LanguageCode, "en")}
		}

		var expanded []Voice
		seen := map[string]bool{}
		for _, code := range codes {
			code = strings.ToLower(strings.TrimSpace(code))
			if code == "" || hiddenSupertonicLanguageCodes[code] || seen[code] {
				continue
			}
			seen[code] = true

			mode := voice
			mode.Engine = supertonicEngine
			mode.LanguageCode = code
			mode.Language = languageName(code, true)
			mode.Quality = "Fast"
			expanded = append(expanded, mode)
		}
		return expanded
	}

	if voice.LanguageCode == "" {
		voice.LanguageCode = detectKokoroLanguageCode(voice.ID)
	}
	if voice.Language == "" || voice.Language == "Multilingual" {
		voice.Language = languageName(voice.LanguageCode, false)
	}
	if voice.Quality == "" {
		voice.Quality = "-"
	}
	return []Voice{voice}
}

func isSupertonicVoice(voice Voice) bool {
	return strings.EqualFold(voice.Engine, supertonicEngine) || strings.HasPrefix(voice.ID, "st_")
}

func languageName(code string, supertonic bool) string {
	if supertonic {
		if name := supertonicLanguageNames[code]; name != "" {
			return name
		}
		return strings.ToUpper(code)
	}
	if name := kokoroLanguageNames[code]; name != "" {
		return name
	}
	return strings.ToUpper(code)
}

func detectKokoroLanguageCode(voiceID string) string {
	switch {
	case strings.HasPrefix(voiceID, "af_"), strings.HasPrefix(voiceID, "am_"):
		return "a"
	case strings.HasPrefix(voiceID, "bf_"), strings.HasPrefix(voiceID, "bm_"):
		return "b"
	case strings.HasPrefix(voiceID, "ef_"), strings.HasPrefix(voiceID, "em_"):
		return "e"
	case strings.HasPrefix(voiceID, "ff_"), strings.HasPrefix(voiceID, "fm_"):
		return "f"
	case strings.HasPrefix(voiceID, "hf_"), strings.HasPrefix(voiceID, "hm_"):
		return "h"
	case strings.HasPrefix(voiceID, "if_"), strings.HasPrefix(voiceID, "im_"):
		return "i"
	case strings.HasPrefix(voiceID, "jf_"), strings.HasPrefix(voiceID, "jm_"):
		return "j"
	case strings.HasPrefix(voiceID, "pf_"), strings.HasPrefix(voiceID, "pm_"):
		return "p"
	case strings.HasPrefix(voiceID, "zf_"), strings.HasPrefix(voiceID, "zm_"):
		return "z"
	default:
		return "a"
	}
}

func stringSlice(value interface{}) []string {
	raw, ok := value.([]interface{})
	if !ok {
		return nil
	}
	codes := make([]string, 0, len(raw))
	for _, item := range raw {
		if code, ok := item.(string); ok {
			codes = append(codes, code)
		}
	}
	return codes
}

func orString(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
