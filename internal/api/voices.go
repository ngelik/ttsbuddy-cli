package api

import (
	"encoding/json"
	"fmt"
)

// Voice represents a TTS voice.
type Voice struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Gender   string `json:"gender"`
	Language string `json:"language"`
}

// CuratedVoices returns the offline-friendly curated voice list.
func CuratedVoices() []Voice {
	return []Voice{
		// American English
		{ID: "af_heart", Name: "Heart", Gender: "Female", Language: "American English"},
		{ID: "af_bella", Name: "Bella", Gender: "Female", Language: "American English"},
		{ID: "af_nicole", Name: "Nicole", Gender: "Female", Language: "American English"},
		{ID: "af_sarah", Name: "Sarah", Gender: "Female", Language: "American English"},
		{ID: "af_alloy", Name: "Alloy", Gender: "Female", Language: "American English"},
		{ID: "am_michael", Name: "Michael", Gender: "Male", Language: "American English"},
		{ID: "am_adam", Name: "Adam", Gender: "Male", Language: "American English"},
		{ID: "am_echo", Name: "Echo", Gender: "Male", Language: "American English"},
		// British English
		{ID: "bf_emma", Name: "Emma", Gender: "Female", Language: "British English"},
		{ID: "bm_george", Name: "George", Gender: "Male", Language: "British English"},
		// Spanish
		{ID: "ef_sofia", Name: "Sofia", Gender: "Female", Language: "Spanish"},
		{ID: "em_diego", Name: "Diego", Gender: "Male", Language: "Spanish"},
		// French
		{ID: "ff_claire", Name: "Claire", Gender: "Female", Language: "French"},
		{ID: "fm_antoine", Name: "Antoine", Gender: "Male", Language: "French"},
		// Hindi
		{ID: "hf_priya", Name: "Priya", Gender: "Female", Language: "Hindi"},
		{ID: "hm_raj", Name: "Raj", Gender: "Male", Language: "Hindi"},
		// Italian
		{ID: "if_lucia", Name: "Lucia", Gender: "Female", Language: "Italian"},
		{ID: "im_marco", Name: "Marco", Gender: "Male", Language: "Italian"},
		// Japanese
		{ID: "jf_yuki", Name: "Yuki", Gender: "Female", Language: "Japanese"},
		{ID: "jm_takeshi", Name: "Takeshi", Gender: "Male", Language: "Japanese"},
		// Portuguese
		{ID: "pf_ana", Name: "Ana", Gender: "Female", Language: "Portuguese"},
		{ID: "pm_carlos", Name: "Carlos", Gender: "Male", Language: "Portuguese"},
		// Chinese
		{ID: "zf_mei", Name: "Mei", Gender: "Female", Language: "Chinese"},
		{ID: "zm_chen", Name: "Chen", Gender: "Male", Language: "Chinese"},
	}
}

// parseVoiceResponse parses the upstream TTS API voice response.
// Tolerant to schema expansion — maps only the fields we need.
func parseVoiceResponse(raw json.RawMessage) ([]Voice, error) {
	// The upstream API may return various structures. Try common shapes.

	// Try array of objects with voice_id / name fields
	var arr []map[string]interface{}
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		var voices []Voice
		for _, item := range arr {
			v := Voice{}
			if id, ok := item["voice_id"].(string); ok {
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
			if v.ID != "" {
				voices = append(voices, v)
			}
		}
		if len(voices) > 0 {
			return voices, nil
		}
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

