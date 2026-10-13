package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const smokeVersion = "0.0.0-smoke"

// smokeBinary builds the CLI, injecting the version through the same -X
// target the release build uses so a renamed variable fails here.
func smokeBinary(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the binary")
	}
	goreleaser, err := os.ReadFile(filepath.Join("..", "..", ".goreleaser.yaml"))
	require.NoError(t, err)
	m := regexp.MustCompile(`-X (\S+)=\{\{\s*\.Version\s*\}\}`).FindSubmatch(goreleaser)
	require.NotNil(t, m, ".goreleaser.yaml has no -X ...={{.Version}} ldflag")

	bin := filepath.Join(t.TempDir(), "doc-scraper")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	out, err := exec.Command("go", "build", "-ldflags", "-X "+string(m[1])+"="+smokeVersion, "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, "go build:\n%s", out)
	return bin
}

func TestSmoke_Version(t *testing.T) {
	bin := smokeBinary(t)

	out, err := exec.Command(bin, "version").Output()

	require.NoError(t, err)
	assert.Equal(t, "doc-scraper "+smokeVersion, strings.TrimSpace(string(out)))
}

func TestSmoke_MCPStdio(t *testing.T) {
	bin := smokeBinary(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(cfg, []byte(`output_base_dir: '`+filepath.ToSlash(filepath.Join(dir, "out"))+`'
state_dir: '`+filepath.ToSlash(filepath.Join(dir, "state"))+`'
sites:
  demo:
    start_urls: ["https://example.com/docs/"]
    allowed_domain: example.com
    content_selector: main
`), 0644))

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "mcp-server", "-config", cfg)
	cmd.Stdin = strings.NewReader(strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
	}, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	require.NoError(t, cmd.Run(), "mcp-server should exit cleanly on stdin EOF; stderr:\n%s", stderr.String())

	var serverVersion string
	var tools []string
	sc := bufio.NewScanner(&stdout)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		var msg struct {
			JSONRPC string `json:"jsonrpc"`
			ID      int    `json:"id"`
			Result  struct {
				ServerInfo struct {
					Version string `json:"version"`
				} `json:"serverInfo"`
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"result"`
		}
		// Anything else on stdout corrupts the stdio transport for clients.
		require.NoError(t, json.Unmarshal(sc.Bytes(), &msg), "non-JSON line on stdout: %q", sc.Text())
		require.Equal(t, "2.0", msg.JSONRPC, "stdout line: %q", sc.Text())
		switch msg.ID {
		case 1:
			serverVersion = msg.Result.ServerInfo.Version
		case 2:
			for _, tool := range msg.Result.Tools {
				tools = append(tools, tool.Name)
			}
		}
	}
	require.NoError(t, sc.Err())

	assert.Equal(t, smokeVersion, serverVersion)
	require.NotEmpty(t, tools)
	sort.Strings(tools)
	assert.Equal(t, tools, helpToolNames(t, bin), "mcp-server -h tool list is out of sync with tools/list")
	assert.Equal(t, tools, docsToolNames(t), "docs mcp/tools.md tool table is out of sync with tools/list")
}

func helpToolNames(t *testing.T, bin string) []string {
	t.Helper()
	out, err := exec.Command(bin, "mcp-server", "-h").CombinedOutput()
	require.NoError(t, err)
	_, after, ok := strings.Cut(string(out), "Available MCP Tools:\n")
	require.True(t, ok, "help has no Available MCP Tools section:\n%s", out)
	var names []string
	for _, line := range strings.Split(after, "\n") {
		if strings.TrimSpace(line) == "" {
			break
		}
		names = append(names, strings.Fields(line)[0])
	}
	sort.Strings(names)
	return names
}
