package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	actionPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
	shaPattern    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

type finding struct {
	File string
	Line int
	Msg  string
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <workflows-dir>\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}

	findings, err := checkWorkflows(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, finding := range findings {
		if finding.Line > 0 {
			fmt.Fprintf(os.Stderr, "%s:%d: %s\n", finding.File, finding.Line, finding.Msg)
			continue
		}
		fmt.Fprintf(os.Stderr, "%s: %s\n", finding.File, finding.Msg)
	}
	if len(findings) > 0 {
		fmt.Fprintln(os.Stderr, "Pin workflow actions to full 40-character commit SHAs.")
		os.Exit(1)
	}
}

func checkWorkflows(workflowsDir string) ([]finding, error) {
	info, err := os.Stat(workflowsDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("GitHub Actions workflows directory is not a directory: %s", workflowsDir)
	}

	var findings []finding
	err = filepath.WalkDir(workflowsDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !isWorkflowFile(path) {
			return nil
		}
		fileFindings, err := checkWorkflowFile(path)
		if err != nil {
			return err
		}
		findings = append(findings, fileFindings...)
		return nil
	})
	return findings, err
}

func isWorkflowFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yml" || ext == ".yaml"
}

func checkWorkflowFile(path string) ([]finding, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var findings []finding
	decoder := yaml.NewDecoder(file)
	for {
		var doc yaml.Node
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			findings = append(findings, finding{File: path, Msg: fmt.Sprintf("invalid workflow YAML: %v", err)})
			break
		}
		walkNode(path, &doc, &findings, map[*yaml.Node]bool{})
	}
	return findings, nil
}

func walkNode(path string, node *yaml.Node, findings *[]finding, seen map[*yaml.Node]bool) {
	if node == nil {
		return
	}
	if seen[node] {
		return
	}
	seen[node] = true
	defer delete(seen, node)

	if node.Kind == yaml.AliasNode {
		walkNode(path, node.Alias, findings, seen)
		return
	}

	switch node.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range node.Content {
			walkNode(path, child, findings, seen)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]
			keyValue, ok, unsupportedTag, tagLine := scalarKeyValue(key, map[*yaml.Node]bool{})
			if unsupportedTag != "" {
				*findings = append(*findings, finding{
					File: path,
					Line: tagLine,
					Msg:  fmt.Sprintf("unsupported scalar YAML key tag %q in workflow", unsupportedTag),
				})
				walkNode(path, value, findings, seen)
				continue
			}
			if !ok {
				*findings = append(*findings, finding{
					File: path,
					Line: key.Line,
					Msg:  "unsupported non-scalar YAML key in workflow",
				})
				walkNode(path, value, findings, seen)
				continue
			}
			if keyValue == "uses" {
				checkUsesValue(path, value, findings)
			}
			walkNode(path, value, findings, seen)
		}
	}
}

func scalarKeyValue(node *yaml.Node, seen map[*yaml.Node]bool) (string, bool, string, int) {
	if node == nil {
		return "", false, "", 0
	}
	if seen[node] {
		return "", false, "", node.Line
	}
	seen[node] = true

	if node.Kind == yaml.AliasNode {
		value, ok, tag, line := scalarKeyValue(node.Alias, seen)
		if line == 0 {
			line = node.Line
		}
		return value, ok, tag, line
	}
	if node.Kind != yaml.ScalarNode {
		return "", false, "", node.Line
	}
	if node.Tag != "" && node.Tag != "!!str" {
		return "", false, node.Tag, node.Line
	}
	return node.Value, true, "", node.Line
}

func scalarValue(node *yaml.Node, seen map[*yaml.Node]bool) (string, bool) {
	if node == nil {
		return "", false
	}
	if seen[node] {
		return "", false
	}
	seen[node] = true

	if node.Kind == yaml.AliasNode {
		return scalarValue(node.Alias, seen)
	}
	if node.Kind != yaml.ScalarNode {
		return "", false
	}
	return node.Value, true
}

func checkUsesValue(path string, valueNode *yaml.Node, findings *[]finding) {
	value, ok := scalarValue(valueNode, map[*yaml.Node]bool{})
	if !ok {
		*findings = append(*findings, finding{
			File: path,
			Line: valueNode.Line,
			Msg:  "unsupported non-scalar GitHub Action reference",
		})
		return
	}
	if msg := validateActionRef(value); msg != "" {
		*findings = append(*findings, finding{
			File: path,
			Line: valueNode.Line,
			Msg:  msg,
		})
	}
}

func validateActionRef(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "empty GitHub Action reference"
	}
	if strings.HasPrefix(value, "./") {
		return ""
	}
	if strings.HasPrefix(value, "docker://") {
		return fmt.Sprintf("unsupported GitHub Action reference: %s", value)
	}
	if strings.ContainsAny(value, " \t\r\n") {
		return fmt.Sprintf("unsupported GitHub Action reference: %s", value)
	}
	if !strings.Contains(value, "@") {
		return fmt.Sprintf("unpinned GitHub Action reference: %s", value)
	}

	action, ref, _ := strings.Cut(value, "@")
	if !actionPattern.MatchString(action) {
		return fmt.Sprintf("unsupported GitHub Action reference: %s", value)
	}
	if !shaPattern.MatchString(ref) {
		return fmt.Sprintf("unpinned GitHub Action reference: %s", value)
	}
	return ""
}
