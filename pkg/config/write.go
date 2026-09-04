package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// RenderSiteEntry renders one site entry as YAML (2-space indent, no leading
// indentation), exactly as InsertSite will add it to the config file.
func RenderSiteEntry(key string, site *SiteConfig) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]*SiteConfig{key: site}); err != nil {
		return "", fmt.Errorf("render site entry: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("render site entry: %w", err)
	}
	return buf.String(), nil
}

// InsertSite adds a site entry to the YAML config at path, creating the file
// if needed. The existing file is preserved byte-for-byte outside the inserted
// lines: the sites mapping is located via the YAML AST, the rendered entry is
// spliced in as text, and the result is re-parsed and re-validated before an
// atomic write. Returns an error if the key already exists.
func InsertSite(path, key string, site *SiteConfig) error {
	entry, err := RenderSiteEntry(key, site)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return writeValidated(path, []byte("sites:\n"+indentBlock(entry, 2)), key, 0o644)
	}
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}

	updated, err := spliceSiteEntry(data, key, entry)
	if err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if fi, statErr := os.Stat(path); statErr == nil {
		perm = fi.Mode().Perm()
	}
	return writeValidated(path, updated, key, perm)
}

func spliceSiteEntry(data []byte, key, entry string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	lines := strings.Split(string(data), "\n")

	root := documentRoot(&doc)
	if root == nil {
		return []byte(strings.TrimRight(string(data), "\n") + "\nsites:\n" + indentBlock(entry, 2)), nil
	}
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("config root is not a YAML mapping")
	}

	keyNode, valNode := mappingValue(root, "sites")
	if keyNode == nil {
		return append(bytes.TrimRight(data, "\n"), []byte("\n\nsites:\n"+indentBlock(entry, 2))...), nil
	}
	if valNode.Kind == yaml.MappingNode && valNode.Style&yaml.FlowStyle != 0 {
		return nil, fmt.Errorf("the sites entry uses YAML flow style ({...}); convert it to block style before using add")
	}

	indent := 2
	insertAfter := keyNode.Line
	if valNode.Kind == yaml.MappingNode && len(valNode.Content) > 0 {
		for i := 0; i < len(valNode.Content); i += 2 {
			if valNode.Content[i].Value == key {
				return nil, fmt.Errorf("site key %q already exists in config; pick another with -site", key)
			}
		}
		indent = valNode.Content[0].Column - 1
		insertAfter = maxLine(valNode)
	} else if valNode.Kind == yaml.ScalarNode && valNode.Value != "" && valNode.Tag != "!!null" {
		return nil, fmt.Errorf("the sites entry is not a mapping")
	}

	if insertAfter > len(lines) {
		insertAfter = len(lines)
	}
	block := indentBlock(entry, indent)
	out := strings.Join(lines[:insertAfter], "\n") + "\n" + block
	if rest := strings.Join(lines[insertAfter:], "\n"); rest != "" {
		out += rest
	}
	return []byte(out), nil
}

func writeValidated(path string, content []byte, key string, perm os.FileMode) error {
	var check AppConfig
	if err := yaml.Unmarshal(content, &check); err != nil {
		return fmt.Errorf("the updated config would not parse (left untouched): %w", err)
	}
	if _, ok := check.Sites[key]; !ok {
		return fmt.Errorf("the updated config would not contain site %q (left untouched)", key)
	}
	if _, err := check.Validate(); err != nil {
		return fmt.Errorf("the updated config would not validate (left untouched): %w", err)
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return nil
}

func mappingValue(mapping *yaml.Node, key string) (*yaml.Node, *yaml.Node) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i], mapping.Content[i+1]
		}
	}
	return nil, nil
}

func maxLine(n *yaml.Node) int {
	line := n.Line
	for _, c := range n.Content {
		if l := maxLine(c); l > line {
			line = l
		}
	}
	return line
}

func indentBlock(block string, spaces int) string {
	pad := strings.Repeat(" ", spaces)
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			b.WriteString("\n")
			continue
		}
		b.WriteString(pad + line + "\n")
	}
	return b.String()
}
