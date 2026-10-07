package fetch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http/httpproxy"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// fakeProxy records what it is asked for. Plain HTTP requests get a 200;
// CONNECT tunnels are refused with 403, which still proves the proxy was dialed.
type fakeProxy struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	redirect string
}

func newFakeProxy(t *testing.T) *fakeProxy {
	t.Helper()
	p := &fakeProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.Method+" "+r.RequestURI)
		redirect := p.redirect
		p.mu.Unlock()
		switch {
		case r.Method == http.MethodConnect:
			w.WriteHeader(http.StatusForbidden)
		case redirect != "":
			http.Redirect(w, r, redirect, http.StatusFound)
		default:
			_, _ = io.WriteString(w, "via proxy")
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProxy) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

var errNoSuchHost = errors.New("no such host")

func fakeLookup(_ context.Context, host string) ([]netip.Addr, error) {
	switch host {
	case "public.test":
		return []netip.Addr{netip.MustParseAddr("93.184.215.14")}, nil
	case "internal.test":
		return []netip.Addr{netip.MustParseAddr("10.0.0.7")}, nil
	case "mixed.test":
		return []netip.Addr{netip.MustParseAddr("93.184.215.14"), netip.MustParseAddr("192.168.1.9")}, nil
	}
	return nil, errNoSuchHost
}

func proxiedClient(t *testing.T, p *fakeProxy, allowPrivate bool, noProxy string) *http.Client {
	t.Helper()
	cfg := config.HTTPClientConfig{AllowPrivateNetworks: allowPrivate, MaxIdleConnsPerHost: 2}
	proxyCfg := &httpproxy.Config{HTTPProxy: p.URL, HTTPSProxy: p.URL, NoProxy: noProxy}
	return newClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), proxyCfg, fakeLookup)
}

func get(c *http.Client, target string) (string, error) {
	resp, err := c.Get(target)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestProxy_PrivateProxyReachesPublicTargets(t *testing.T) {
	p := newFakeProxy(t)
	c := proxiedClient(t, p, false, "")

	body, err := get(c, "http://93.184.215.14/docs/")
	require.NoError(t, err)
	assert.Equal(t, "via proxy", body)

	body, err = get(c, "http://public.test/docs/")
	require.NoError(t, err)
	assert.Equal(t, "via proxy", body)

	assert.Equal(t, []string{"GET http://93.184.215.14/docs/", "GET http://public.test/docs/"}, p.seen())
}

func TestProxy_HTTPSTunnelToPublicTargetReachesProxy(t *testing.T) {
	p := newFakeProxy(t)
	c := proxiedClient(t, p, false, "")

	_, err := get(c, "https://public.test/docs/")

	require.Error(t, err, "fake proxy refuses CONNECT")
	require.NotErrorIs(t, err, utils.ErrBlockedAddress)
	assert.Equal(t, []string{"CONNECT public.test:443"}, p.seen())
}

func TestProxy_BlocksPrivateTargetsBeforeContactingProxy(t *testing.T) {
	for _, target := range []string{
		"http://10.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[fd00::1]/",
		"http://100.64.0.1/",
		"http://internal.test/",
		"http://mixed.test/",
		"https://10.0.0.1/",
		"https://internal.test/",
	} {
		t.Run(target, func(t *testing.T) {
			p := newFakeProxy(t)
			c := proxiedClient(t, p, false, "")

			_, err := get(c, target)

			require.ErrorIs(t, err, utils.ErrBlockedAddress)
			assert.Empty(t, p.seen(), "blocked target must never reach the proxy")
		})
	}
}

func TestProxy_UnresolvableTargetFailsClosed(t *testing.T) {
	p := newFakeProxy(t)
	c := proxiedClient(t, p, false, "")

	_, err := get(c, "http://nowhere.test/")

	require.ErrorIs(t, err, errNoSuchHost)
	assert.Empty(t, p.seen())
}

func TestProxy_RedirectToPrivateTargetBlocked(t *testing.T) {
	p := newFakeProxy(t)
	p.redirect = "http://10.0.0.1/admin"
	c := proxiedClient(t, p, false, "")

	_, err := get(c, "http://public.test/")

	require.ErrorIs(t, err, utils.ErrBlockedAddress)
	assert.Equal(t, []string{"GET http://public.test/"}, p.seen(), "only the first hop reaches the proxy")
}

func TestProxy_AllowPrivateNetworksSkipsTargetCheck(t *testing.T) {
	p := newFakeProxy(t)
	c := proxiedClient(t, p, true, "")

	body, err := get(c, "http://10.0.0.1/")

	require.NoError(t, err)
	assert.Equal(t, "via proxy", body)
}

func TestProxy_NoProxyTargetsStillGuardedDirectly(t *testing.T) {
	p := newFakeProxy(t)
	c := proxiedClient(t, p, false, "10.0.0.0/8")

	_, err := get(c, "http://10.0.0.1/")

	require.ErrorIs(t, err, utils.ErrBlockedAddress)
	assert.Empty(t, p.seen())
}

// The proxy's own address is exempt from the guard only when dialed as the
// proxy. Requests that target it directly (loopback is never proxied) must
// still be blocked, including after the exemption has been recorded.
func TestProxy_DirectRequestToProxyAddressGuarded(t *testing.T) {
	p := newFakeProxy(t)
	c := proxiedClient(t, p, false, "")
	_, err := get(c, "http://public.test/")
	require.NoError(t, err)

	_, err = get(c, p.URL+"/")

	require.ErrorIs(t, err, utils.ErrBlockedAddress)
	assert.Equal(t, []string{"GET http://public.test/"}, p.seen())
}

// net/http dials the proxy host exactly as written when it is ASCII, so the
// recorded exemption must not normalize its case.
func TestProxy_MixedCaseProxyHostStillExempt(t *testing.T) {
	p := newFakeProxy(t)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(p.URL, "http://"))
	require.NoError(t, err)
	cfg := config.HTTPClientConfig{MaxIdleConnsPerHost: 2}
	proxyCfg := &httpproxy.Config{HTTPProxy: "http://LocalHost:" + port}
	c := newClient(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), proxyCfg, fakeLookup)

	body, err := get(c, "http://public.test/")

	require.NoError(t, err)
	assert.Equal(t, "via proxy", body)
}
