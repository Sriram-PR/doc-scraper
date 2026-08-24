package process

import (
	"errors"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/PuerkitoBio/goquery"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakePageStore records the URLs passed to MarkPageVisited. Any URL in `seen`
// is reported as already-visited; any URL in `failURLs` returns an error.
type fakePageStore struct {
	mu       sync.Mutex
	marked   []string
	seen     map[string]bool
	failURLs map[string]bool
}

func newFakePageStore() *fakePageStore {
	return &fakePageStore{seen: map[string]bool{}, failURLs: map[string]bool{}}
}

func (f *fakePageStore) MarkPageVisited(u string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, u)
	if f.failURLs[u] {
		return false, errors.New("boom")
	}
	if f.seen[u] {
		return false, nil
	}
	f.seen[u] = true
	return true, nil
}

func (f *fakePageStore) CheckPageStatus(string) (models.PageStatus, *models.PageDBEntry, error) {
	return models.PageStatusNotFound, nil, nil
}
func (f *fakePageStore) UpdatePageStatus(string, *models.PageDBEntry) error { return nil }
func (f *fakePageStore) GetPageContentHash(string) (string, bool, error)    { return "", false, nil }

// queuedURLs drains the queue and returns the URLs in sorted order.
func queuedURLs(t *testing.T, pq *queue.ThreadSafePriorityQueue) []string {
	t.Helper()
	var got []string
	for pq.Len() > 0 {
		item, ok := pq.Pop()
		if !ok {
			break
		}
		got = append(got, item.URL)
	}
	sort.Strings(got)
	return got
}

// runExtract wires a LinkProcessor over the given HTML and returns the queued
// count, the queued URLs, and any error.
func runExtract(t *testing.T, html string, pageURL string, siteCfg *config.SiteConfig, store *fakePageStore, depth int, disallowed []*regexp.Regexp) (int, []string, error) {
	t.Helper()

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	finalURL, err := url.Parse(pageURL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	pq := queue.NewThreadSafePriorityQueue(silentLog())
	lp := NewLinkProcessor(store, pq, disallowed, silentLog())

	var wg sync.WaitGroup
	n, extractErr := lp.ExtractAndQueueLinks(doc, finalURL, depth, siteCfg, &wg, silentLog())
	for range n {
		wg.Done() // ExtractAndQueueLinks Adds one per queued item; the crawler workers normally Done them.
	}
	wg.Wait()

	return n, queuedURLs(t, pq), extractErr
}

func baseSiteCfg() *config.SiteConfig {
	return &config.SiteConfig{
		AllowedDomain:     "docs.example.com",
		AllowedPathPrefix: "/",
	}
}

func TestIsResourceURL(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/img/logo.png", true},
		{"/img/logo.PNG", true},
		{"/style.css", true},
		{"/app.js", true},
		{"/paper.pdf", true},
		{"/archive.tar.gz", true},
		{"/video.mp4", true},
		{"/font.woff2", true},
		{"/guide/index.html", false},
		{"/guide/", false},
		{"/guide", false},
		{"/data.json", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			u, err := url.Parse("https://docs.example.com" + tt.path)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := isResourceURL(u); got != tt.want {
				t.Errorf("isResourceURL(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestExtractAndQueueLinks_QueuesInScopeLinks(t *testing.T) {
	html := `<body>
		<a href="/guide/a.html">A</a>
		<a href="b.html">B</a>
		<a href="https://docs.example.com/guide/c.html">C</a>
	</body>`

	store := newFakePageStore()
	n, got, err := runExtract(t, html, "https://docs.example.com/guide/", baseSiteCfg(), store, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 3 {
		t.Fatalf("queued %d links, want 3 (got %v)", n, got)
	}
	want := []string{
		"https://docs.example.com/guide/a.html",
		"https://docs.example.com/guide/b.html",
		"https://docs.example.com/guide/c.html",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("queued[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestExtractAndQueueLinks_FiltersOutOfScope(t *testing.T) {
	tests := []struct {
		name string
		html string
	}{
		{"OffDomain", `<a href="https://evil.example.org/x.html">x</a>`},
		{"Subdomain", `<a href="https://other.example.com/x.html">x</a>`},
		{"MailtoScheme", `<a href="mailto:hi@example.com">mail</a>`},
		{"JavascriptScheme", `<a href="javascript:void(0)">js</a>`},
		{"ResourceExtension", `<a href="/guide/logo.png">img</a>`},
		{"EmptyHref", `<a href="">empty</a>`},
		{"StylesheetExtension", `<a href="/guide/theme.css">css</a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakePageStore()
			n, _, err := runExtract(t, "<body>"+tt.html+"</body>", "https://docs.example.com/guide/", baseSiteCfg(), store, 0, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n != 0 {
				t.Errorf("queued %d links, want 0", n)
			}
		})
	}
}

func TestExtractAndQueueLinks_PathPrefixFilter(t *testing.T) {
	html := `<body>
		<a href="/guide/in.html">in</a>
		<a href="/blog/out.html">out</a>
	</body>`

	siteCfg := baseSiteCfg()
	siteCfg.AllowedPathPrefix = "/guide/"

	store := newFakePageStore()
	n, got, err := runExtract(t, html, "https://docs.example.com/guide/", siteCfg, store, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 || got[0] != "https://docs.example.com/guide/in.html" {
		t.Errorf("queued %v (n=%d), want only the /guide/ link", got, n)
	}
}

func TestExtractAndQueueLinks_DisallowedPatterns(t *testing.T) {
	html := `<body>
		<a href="/guide/keep.html">keep</a>
		<a href="/guide/print.html">print</a>
	</body>`

	disallowed := []*regexp.Regexp{regexp.MustCompile(`/print\.html$`)}

	store := newFakePageStore()
	n, got, err := runExtract(t, html, "https://docs.example.com/guide/", baseSiteCfg(), store, 0, disallowed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 || got[0] != "https://docs.example.com/guide/keep.html" {
		t.Errorf("queued %v (n=%d), want only keep.html", got, n)
	}
}

func TestExtractAndQueueLinks_MaxDepth(t *testing.T) {
	html := `<body><a href="/guide/next.html">next</a></body>`

	tests := []struct {
		name         string
		maxDepth     int
		currentDepth int
		wantQueued   int
	}{
		{"UnlimitedDepth", 0, 99, 1},
		{"NextDepthWithinLimit", 2, 0, 1},
		{"NextDepthAtLimit", 1, 0, 1},
		{"NextDepthExceedsLimit", 1, 1, 0},
		{"WellPastLimit", 2, 5, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			siteCfg := baseSiteCfg()
			siteCfg.MaxDepth = tt.maxDepth

			store := newFakePageStore()
			n, _, err := runExtract(t, html, "https://docs.example.com/guide/", siteCfg, store, tt.currentDepth, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if n != tt.wantQueued {
				t.Errorf("queued %d, want %d", n, tt.wantQueued)
			}
		})
	}
}

func TestExtractAndQueueLinks_RespectNofollow(t *testing.T) {
	html := `<body>
		<a href="/guide/plain.html">plain</a>
		<a href="/guide/nf.html" rel="nofollow">nf</a>
		<a href="/guide/mixed.html" rel="noopener NOFOLLOW">mixed</a>
	</body>`

	t.Run("Enabled", func(t *testing.T) {
		siteCfg := baseSiteCfg()
		siteCfg.RespectNofollow = true

		store := newFakePageStore()
		n, got, err := runExtract(t, html, "https://docs.example.com/guide/", siteCfg, store, 0, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 1 || got[0] != "https://docs.example.com/guide/plain.html" {
			t.Errorf("queued %v (n=%d), want only plain.html", got, n)
		}
	})

	t.Run("Disabled", func(t *testing.T) {
		store := newFakePageStore()
		n, _, err := runExtract(t, html, "https://docs.example.com/guide/", baseSiteCfg(), store, 0, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 3 {
			t.Errorf("queued %d, want 3 when respect_nofollow is off", n)
		}
	})
}

// Fragments must be stripped before the dedup check, otherwise a same-page
// anchor is enqueued as a distinct page.
func TestExtractAndQueueLinks_NormalizesFragmentsBeforeDedup(t *testing.T) {
	html := `<body>
		<a href="/guide/a.html">a</a>
		<a href="/guide/a.html#section-one">a anchor</a>
		<a href="/guide/a.html#section-two">a other anchor</a>
	</body>`

	store := newFakePageStore()
	n, got, err := runExtract(t, html, "https://docs.example.com/guide/", baseSiteCfg(), store, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Fatalf("queued %d links, want 1 (got %v)", n, got)
	}
	if strings.Contains(got[0], "#") {
		t.Errorf("queued URL %q still carries a fragment", got[0])
	}
}

func TestExtractAndQueueLinks_SkipsAlreadyVisited(t *testing.T) {
	html := `<body>
		<a href="/guide/new.html">new</a>
		<a href="/guide/old.html">old</a>
	</body>`

	store := newFakePageStore()
	store.seen["https://docs.example.com/guide/old.html"] = true

	n, got, err := runExtract(t, html, "https://docs.example.com/guide/", baseSiteCfg(), store, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 || got[0] != "https://docs.example.com/guide/new.html" {
		t.Errorf("queued %v (n=%d), want only new.html", got, n)
	}
	if len(store.marked) != 2 {
		t.Errorf("MarkPageVisited called %d times, want 2", len(store.marked))
	}
}

// A DB failure on one link must not abort the others, and the first error is returned.
func TestExtractAndQueueLinks_DBErrorIsNonFatal(t *testing.T) {
	html := `<body>
		<a href="/guide/ok1.html">ok1</a>
		<a href="/guide/bad.html">bad</a>
		<a href="/guide/ok2.html">ok2</a>
	</body>`

	store := newFakePageStore()
	store.failURLs["https://docs.example.com/guide/bad.html"] = true

	n, got, err := runExtract(t, html, "https://docs.example.com/guide/", baseSiteCfg(), store, 0, nil)
	if err == nil {
		t.Fatal("expected a DB error to be returned")
	}
	if !errors.Is(err, utils.ErrDatabase) {
		t.Errorf("error = %v, want wrapped ErrDatabase", err)
	}
	if n != 2 {
		t.Errorf("queued %d links, want 2 (got %v)", n, got)
	}
}

func TestExtractAndQueueLinks_UsesLinkExtractionSelectors(t *testing.T) {
	html := `<body>
		<nav id="sidebar"><a href="/guide/nav.html">nav</a></nav>
		<main><a href="/guide/main.html">main</a></main>
		<footer><a href="/guide/footer.html">footer</a></footer>
	</body>`

	t.Run("ScopedToSelectors", func(t *testing.T) {
		siteCfg := baseSiteCfg()
		siteCfg.LinkExtractionSelectors = []string{"nav#sidebar", "main"}

		store := newFakePageStore()
		n, got, err := runExtract(t, html, "https://docs.example.com/guide/", siteCfg, store, 0, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 2 {
			t.Fatalf("queued %d links, want 2 (got %v)", n, got)
		}
		for _, u := range got {
			if strings.Contains(u, "footer") {
				t.Errorf("footer link %q should not be queued", u)
			}
		}
	})

	t.Run("DefaultsToBody", func(t *testing.T) {
		store := newFakePageStore()
		n, _, err := runExtract(t, html, "https://docs.example.com/guide/", baseSiteCfg(), store, 0, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != 3 {
			t.Errorf("queued %d links, want all 3 when no selectors configured", n)
		}
	})
}

// Overlapping selectors must not enqueue the same link twice.
func TestExtractAndQueueLinks_DedupesAcrossOverlappingSelectors(t *testing.T) {
	html := `<body><main><a href="/guide/dup.html">dup</a></main></body>`

	siteCfg := baseSiteCfg()
	siteCfg.LinkExtractionSelectors = []string{"body", "main"}

	store := newFakePageStore()
	n, got, err := runExtract(t, html, "https://docs.example.com/guide/", siteCfg, store, 0, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 1 {
		t.Errorf("queued %d links, want 1 (got %v)", n, got)
	}
}

func TestExtractAndQueueLinks_QueuedItemsCarryNextDepth(t *testing.T) {
	html := `<body><a href="/guide/next.html">next</a></body>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	finalURL, _ := url.Parse("https://docs.example.com/guide/")

	pq := queue.NewThreadSafePriorityQueue(silentLog())
	lp := NewLinkProcessor(newFakePageStore(), pq, nil, silentLog())

	var wg sync.WaitGroup
	n, err := lp.ExtractAndQueueLinks(doc, finalURL, 3, baseSiteCfg(), &wg, silentLog())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for range n {
		wg.Done()
	}

	item, ok := pq.Pop()
	if !ok {
		t.Fatal("expected a queued item")
	}
	if item.Depth != 4 {
		t.Errorf("queued item depth = %d, want 4 (currentDepth+1)", item.Depth)
	}
}
