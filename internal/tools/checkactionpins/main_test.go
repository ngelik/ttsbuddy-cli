package main

import (
	"os"
	"path/filepath"
	"testing"
)

const pinnedCheckout = "actions/checkout@df4cb1c069e1874edd31b4311f1884172cec0e10"

func TestCheckWorkflows(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{
			name: "pinned action",
			body: workflowWithStep("- uses: " + pinnedCheckout),
		},
		{
			name: "quoted pinned value",
			body: workflowWithStep("- uses: \"" + pinnedCheckout + "\""),
		},
		{
			name: "quoted pinned key",
			body: workflowWithStep("- \"uses\": " + pinnedCheckout),
		},
		{
			name: "local action",
			body: workflowWithStep("- uses: ./local-action"),
		},
		{
			name: "uses text inside run block",
			body: workflowWithStep("- run: |\n          echo \"uses: actions/checkout@v6\"\n          echo '\"u\\x73es\": actions/checkout@v6'"),
		},
		{
			name:    "mutable tag",
			body:    workflowWithStep("- uses: actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "spaced key",
			body:    workflowWithStep("- uses : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "empty uses",
			body:    workflowWithStep("- uses:"),
			wantErr: true,
		},
		{
			name:    "block scalar uses",
			body:    workflowWithStep("- uses: >\n          actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "docker action",
			body:    workflowWithStep("- uses: docker://alpine:3.20"),
			wantErr: true,
		},
		{
			name:    "flow style step",
			body:    workflowWithRawSteps("{ uses: actions/checkout@v6 }"),
			wantErr: true,
		},
		{
			name:    "flow style reusable workflow job",
			body:    "name: x\non: push\njobs: { call: { uses: owner/repo/.github/workflows/build.yml@v1 } }\n",
			wantErr: true,
		},
		{
			name:    "quoted key tag",
			body:    workflowWithStep("- \"uses\": actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "explicit key",
			body:    workflowWithStep("- ? uses\n        : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "inline explicit key",
			body:    workflowWithStep("- ? uses : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "anchored key",
			body:    workflowWithStep("- &checkout uses: actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "tagged key",
			body:    workflowWithStep("- !tag uses: actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "escaped quoted key",
			body:    workflowWithStep("- \"u\\x73es\": actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "escaped flow key",
			body:    workflowWithRawSteps("{ \"u\\x73es\": actions/checkout@v6 }"),
			wantErr: true,
		},
		{
			name:    "escaped explicit key",
			body:    workflowWithStep("- ? \"u\\x73es\"\n        : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "binary-tagged key",
			body:    workflowWithStep("- !!binary dXNlcw==: actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "binary-tagged explicit key",
			body:    workflowWithStep("- ? !!binary dXNlcw==\n        : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "multiline explicit key",
			body:    workflowWithStep("- ?\n          uses\n        : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "alias explicit key",
			body:    "name: x\non: push\nx-key: &u uses\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n      - ? *u\n        : actions/checkout@v6\n",
			wantErr: true,
		},
		{
			name:    "tagged multiline explicit key",
			body:    workflowWithStep("- ?\n          !!str uses\n        : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name:    "anchored multiline explicit key",
			body:    workflowWithStep("- ?\n          &k uses\n        : actions/checkout@v6"),
			wantErr: true,
		},
		{
			name: "pinned value alias",
			body: "name: x\non: push\nx-ref: &checkout " + pinnedCheckout + "\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: *checkout\n",
		},
		{
			name:    "mutable value alias",
			body:    "name: x\non: push\nx-ref: &checkout actions/checkout@v6\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n      - uses: *checkout\n",
			wantErr: true,
		},
		{
			name:    "non scalar uses value",
			body:    workflowWithStep("- uses:\n          action: " + pinnedCheckout),
			wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			workflowDir := filepath.Join(dir, ".github", "workflows")
			if err := os.MkdirAll(workflowDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workflowDir, "test.yml"), []byte(test.body), 0o644); err != nil {
				t.Fatal(err)
			}

			findings, err := checkWorkflows(workflowDir)
			if err != nil {
				t.Fatal(err)
			}
			gotErr := len(findings) > 0
			if gotErr != test.wantErr {
				t.Fatalf("got findings=%v, wantErr=%v", findings, test.wantErr)
			}
		})
	}
}

func workflowWithStep(step string) string {
	return "name: x\non: push\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps:\n      " + step + "\n"
}

func workflowWithRawSteps(step string) string {
	return "name: x\non: push\njobs:\n  t:\n    runs-on: ubuntu-latest\n    steps: [ " + step + " ]\n"
}
