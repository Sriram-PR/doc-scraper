package mcp

import (
	"fmt"
	"math"
	"sort"

	"github.com/mark3labs/mcp-go/mcp"
)

type argKind int

const (
	argString argKind = iota
	argBool
	argInteger
)

func (k argKind) String() string {
	switch k {
	case argBool:
		return "boolean"
	case argInteger:
		return "integer"
	default:
		return "string"
	}
}

// checkArgTypes rejects a present, non-null argument whose JSON type differs
// from the declared one. The mcp-go Get* accessors silently coerce or fall
// back to defaults, which would e.g. run a non-incremental crawl for
// incremental:"yes". Numeric strings are rejected for integers.
func checkArgTypes(req mcp.CallToolRequest, spec map[string]argKind) *mcp.CallToolResult {
	args := req.GetArguments()
	names := make([]string, 0, len(spec))
	for name := range spec {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		kind := spec[name]
		v, ok := args[name]
		if !ok || v == nil {
			continue
		}
		valid := false
		switch kind {
		case argString:
			_, valid = v.(string)
		case argBool:
			_, valid = v.(bool)
		case argInteger:
			switch n := v.(type) {
			case float64:
				valid = n == math.Trunc(n) && math.Abs(n) <= 1<<53
			case int:
				valid = true
			}
		}
		if !valid {
			return mcp.NewToolResultError(fmt.Sprintf("argument %q must be %s", name, kind))
		}
	}
	return nil
}
