package config

import (
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// UnknownKeys returns one message per YAML key that does not map to a config
// field. Loading stays lenient; callers surface these as warnings.
func UnknownKeys(data []byte) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil
	}
	root := documentRoot(&doc)
	if root == nil {
		return nil
	}
	var out []string
	walkUnknown(root, reflect.TypeOf(AppConfig{}), "", &out)
	return out
}

func walkUnknown(n *yaml.Node, t reflect.Type, path string, out *[]string) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if n.Kind != yaml.MappingNode {
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			ft, ok := fields[k.Value]
			if !ok {
				*out = append(*out, fmt.Sprintf("line %d: unknown key %q", k.Line, joinPath(path, k.Value)))
				continue
			}
			walkUnknown(v, ft, joinPath(path, k.Value), out)
		}
	case reflect.Map:
		for i := 0; i+1 < len(n.Content); i += 2 {
			walkUnknown(n.Content[i+1], t.Elem(), joinPath(path, n.Content[i].Value), out)
		}
	}
}

func yamlFields(t reflect.Type) map[string]reflect.Type {
	m := make(map[string]reflect.Type, t.NumField())
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		m[name] = f.Type
	}
	return m
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}
