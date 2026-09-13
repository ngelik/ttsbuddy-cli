package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// Exercise real Cobra subprocesses and captured HTTP bodies, including both
// submission paths. Metadata must not change the content idempotency identity.
func TestSubmissionClientContextTransport(t *testing.T) {
	for _, command := range []string{"speak", "web"} {
		t.Run(command, func(t *testing.T) {
			type submission struct {
				body           map[string]any
				key, userAgent string
			}
			received := make(chan submission, 8)
			server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				received <- submission{body, r.Header.Get("Idempotency-Key"), r.Header.Get("User-Agent")}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"status":"completed","job_id":"context-job","audio_url":"https://example.amazonaws.com/audio.mp3"}`))
			}))
			cases := []struct{ name, env, flag, want string }{
				{name: "default", want: "unknown"},
				{name: "environment", env: "automation", want: "automation_declared"},
				{name: "flag overrides environment", env: "automation", flag: "agent", want: "agent_declared"},
				{name: "human", flag: "human", want: "human_declared"},
				{name: "unknown overrides environment", env: "agent", flag: "unknown", want: "unknown"},
				{name: "valid flag overrides invalid environment", env: "robot", flag: "agent", want: "agent_declared"},
			}
			firstKey := ""
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					env := append(envForTest(t.TempDir(), server, "ttsb_test_key"), "TTSBUDDY_EXECUTION_CONTEXT="+tc.env, "TTSBUDDY_TEST_FAKE_WEB_ARTICLE=1")
					input := "Attribution transport test"
					if command == "web" {
						input = "https://example.com/article"
					}
					args := []string{command, input, "--no-download", "--json", "--voice", "st_m1", "--language", "en", "--speed", "1"}
					if tc.flag != "" {
						args = append(args, "--execution-context", tc.flag)
					}
					result := runCLI(t, env, args...)
					assertExitCode(t, result, 0)
					select {
					case got := <-received:
						context, ok := got.body["client_context"].(map[string]any)
						if !ok || context["client"] != "cli" || context["execution_context"] != tc.want {
							t.Fatalf("context = %#v, want cli/%s", got.body["client_context"], tc.want)
						}
						version, ok := context["version"].(string)
						if !ok || version == "" || got.userAgent != "ttsbuddy-cli/"+version {
							t.Fatalf("version = %#v; User-Agent = %q", context["version"], got.userAgent)
						}
						if got.key == "" {
							t.Fatal("missing idempotency key")
						}
						if firstKey == "" {
							firstKey = got.key
						} else if got.key != firstKey {
							t.Fatalf("context changed idempotency: %q != %q", got.key, firstKey)
						}
						if command == "web" && got.body["source"] != "webpage" {
							t.Fatalf("source = %#v", got.body["source"])
						}
					default:
						t.Fatal("command did not submit")
					}
				})
			}
		})
	}
}

func TestInvalidSubmissionContextNeverSubmits(t *testing.T) {
	var calls atomic.Int32
	server := startMockAPI(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	for _, command := range []string{"speak", "web"} {
		for _, useFlag := range []bool{false, true} {
			name := command + "/environment"
			if useFlag {
				name = command + "/flag"
			}
			t.Run(name, func(t *testing.T) {
				env := append(envForTest(t.TempDir(), server, "ttsb_test_key"), "TTSBUDDY_EXECUTION_CONTEXT=robot")
				input := "test"
				if command == "web" {
					input = "https://example.com/article"
				}
				args := []string{command, input, "--json", "--no-download"}
				if useFlag {
					env = append(env, "TTSBUDDY_EXECUTION_CONTEXT=agent")
					args = append(args, "--execution-context", "robot")
				}
				got := runCLI(t, env, args...)
				assertExitCode(t, got, 2)
				var payload struct {
					Error struct {
						Code       string `json:"code"`
						Reason     string `json:"reason"`
						NextAction string `json:"next_action"`
						Retryable  bool   `json:"retryable"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(got.Stdout), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Error.Code != "CLI_ERROR" || payload.Error.Reason != "INVALID_EXECUTION_CONTEXT" || payload.Error.NextAction == "" || payload.Error.Retryable {
					t.Fatalf("unexpected recovery contract: %#v", payload)
				}
				if !strings.Contains(got.Stdout, "invalid execution context") {
					t.Fatalf("missing actionable error: %s", got.Stdout)
				}
				assertValidJSON(t, got.Stdout)
				if calls.Load() != 0 {
					t.Fatal("invalid declaration reached API")
				}
			})
		}
	}
}
