package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

func TestMatchDomain(t *testing.T) {
	tests := []struct {
		host    string
		pattern string
		want    bool
	}{
		{"example.com", "example.com", true},
		{"EXAMPLE.com", "example.com", true},
		{"example.com", "EXAMPLE.COM", true},
		{"other.com", "example.com", false},
		{"sub.example.com", "example.com", false},
		{"cdn.example.com", "*.example.com", true},
		{"a.b.example.com", "*.example.com", true},
		{"example.com", "*.example.com", true}, // wildcard also matches the apex
		{"notexample.com", "*.example.com", false},
		{"example.com.evil.org", "*.example.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.host+"_vs_"+tt.pattern, func(t *testing.T) {
			if got := matchDomain(tt.host, tt.pattern); got != tt.want {
				t.Errorf("matchDomain(%q, %q) = %v, want %v", tt.host, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestIsDomainAllowed(t *testing.T) {
	tests := []struct {
		name       string
		host       string
		allowed    []string
		disallowed []string
		want       bool
	}{
		{"NoFilters", "cdn.example.com", nil, nil, true},
		{"AllowListHit", "cdn.example.com", []string{"cdn.example.com"}, nil, true},
		{"AllowListMiss", "other.com", []string{"cdn.example.com"}, nil, false},
		{"AllowListWildcard", "img.example.com", []string{"*.example.com"}, nil, true},
		{"DisallowHit", "ads.example.com", nil, []string{"ads.example.com"}, false},
		{"DisallowWildcard", "x.ads.com", nil, []string{"*.ads.com"}, false},
		{"DisallowBeatsAllow", "ads.example.com", []string{"*.example.com"}, []string{"ads.example.com"}, false},
		{"AllowedWhileOthersDisallowed", "img.example.com", []string{"*.example.com"}, []string{"ads.example.com"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDomainAllowed(tt.host, tt.allowed, tt.disallowed); got != tt.want {
				t.Errorf("isDomainAllowed(%q, %v, %v) = %v, want %v", tt.host, tt.allowed, tt.disallowed, got, tt.want)
			}
		})
	}
}

func TestGenerateLocalFilename(t *testing.T) {
	tests := []struct {
		name        string
		rawURL      string
		contentType string
		wantPrefix  string
		wantExt     string
		wantErr     bool
	}{
		{"JpegFromContentType", "https://e.com/img/photo.bin", "image/jpeg", "photo_", ".jpg", false},
		{"PngFromContentType", "https://e.com/img/logo", "image/png", "logo_", ".png", false},
		{"GifFromContentType", "https://e.com/img/anim", "image/gif", "anim_", ".gif", false},
		{"WebpFromContentType", "https://e.com/img/pic", "image/webp", "pic_", ".webp", false},
		{"SvgFromContentType", "https://e.com/img/icon", "image/svg+xml", "icon_", ".svg", false},
		{"ContentTypeWithCharset", "https://e.com/img/x", "image/png; charset=binary", "x_", ".png", false},
		{"FallsBackToURLExtension", "https://e.com/img/pic.png", "", "pic_", ".png", false},
		{"UnparsableContentTypeUsesURLExt", "https://e.com/img/pic.png", "not/a/valid/type", "pic_", ".png", false},
		{"NoContentTypeNoExtension", "https://e.com/img/pic", "", "", "", true},
		{"UnparsableContentTypeNoExt", "https://e.com/img/pic", "!!!", "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.rawURL)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			got, err := generateLocalFilename(u, tt.rawURL, tt.contentType, silentLog())
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got filename %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("filename %q, want prefix %q", got, tt.wantPrefix)
			}
			if !strings.HasSuffix(got, tt.wantExt) {
				t.Errorf("filename %q, want extension %q", got, tt.wantExt)
			}
		})
	}
}

// Same base name under different paths must not collide on disk.
func TestGenerateLocalFilename_DisambiguatesSameBaseName(t *testing.T) {
	a := "https://e.com/one/logo.png"
	b := "https://e.com/two/logo.png"

	ua, _ := url.Parse(a)
	ub, _ := url.Parse(b)

	nameA, err := generateLocalFilename(ua, a, "image/png", silentLog())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nameB, err := generateLocalFilename(ub, b, "image/png", silentLog())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nameA == nameB {
		t.Errorf("distinct URLs produced the same filename %q", nameA)
	}
	if !strings.HasPrefix(nameA, "logo_") || !strings.HasPrefix(nameB, "logo_") {
		t.Errorf("expected both to keep the logo_ base: %q %q", nameA, nameB)
	}
}

// A URL with no usable base name still yields a unique filename: SanitizeFilename
// substitutes "untitled" and the URL hash suffix keeps the names distinct.
func TestGenerateLocalFilename_DegenerateBaseNamesStayUnique(t *testing.T) {
	rawA := "https://e.com/"
	rawB := "https://other.com/"

	ua, _ := url.Parse(rawA)
	ub, _ := url.Parse(rawB)

	gotA, err := generateLocalFilename(ua, rawA, "image/png", silentLog())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	gotB, err := generateLocalFilename(ub, rawB, "image/png", silentLog())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.HasPrefix(gotA, "untitled_") {
		t.Errorf("filename %q, want the untitled_ base", gotA)
	}
	if gotA == gotB {
		t.Errorf("distinct degenerate URLs produced the same filename %q", gotA)
	}
}

// imageResponse builds a response suitable for saveImageToDisk.
func imageResponse(body string, contentType string, contentLength string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	if contentLength != "" {
		h.Set("Content-Length", contentLength)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func imageTask(t *testing.T, rawURL string) ImageDownloadTask {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ImageDownloadTask{
		AbsImgURL:   rawURL,
		NormImgURL:  rawURL,
		BaseImgURL:  u,
		ImgHost:     u.Hostname(),
		ImgLogEntry: silentLog(),
		Ctx:         context.Background(),
	}
}

func TestSaveImageToDisk_WritesFileAndReturnsRelativePath(t *testing.T) {
	dir := t.TempDir()
	ip := &ImageProcessor{log: silentLog()}
	task := imageTask(t, "https://e.com/img/logo.png")

	rel, n, err := ip.saveImageToDisk(task, imageResponse("PNGDATA", "image/png", ""), 0, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != int64(len("PNGDATA")) {
		t.Errorf("copied %d bytes, want %d", n, len("PNGDATA"))
	}
	if !strings.HasPrefix(rel, ImageDir+"/") {
		t.Errorf("relative path %q, want it under %q with forward slashes", rel, ImageDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(data) != "PNGDATA" {
		t.Errorf("file contents = %q, want %q", data, "PNGDATA")
	}
}

func TestSaveImageToDisk_RejectsAndRemovesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	ip := &ImageProcessor{log: silentLog()}
	task := imageTask(t, "https://e.com/img/big.png")

	// 20 bytes of body against a 10-byte cap, with no Content-Length to vouch for it.
	_, _, err := ip.saveImageToDisk(task, imageResponse(strings.Repeat("x", 20), "image/png", ""), 10, dir)
	if err == nil {
		t.Fatal("expected an oversize error")
	}
	if !strings.Contains(err.Error(), "exceeds max size") {
		t.Errorf("error = %v, want an exceeds-max-size error", err)
	}

	entries, readErr := os.ReadDir(filepath.Join(dir, ImageDir))
	if readErr != nil {
		t.Fatalf("read image dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("truncated file was left behind: %v", entries)
	}
}

// A file exactly at the limit is kept when Content-Length confirms it was complete.
func TestSaveImageToDisk_KeepsFileExactlyAtLimitWhenContentLengthAgrees(t *testing.T) {
	dir := t.TempDir()
	ip := &ImageProcessor{log: silentLog()}
	task := imageTask(t, "https://e.com/img/exact.png")

	body := strings.Repeat("x", 10)
	rel, n, err := ip.saveImageToDisk(task, imageResponse(body, "image/png", "10"), 10, dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 10 {
		t.Errorf("copied %d bytes, want 10", n)
	}
	if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); statErr != nil {
		t.Errorf("file at exactly the limit should have been kept: %v", statErr)
	}
}

func TestSaveImageToDisk_PropagatesFilenameError(t *testing.T) {
	dir := t.TempDir()
	ip := &ImageProcessor{log: silentLog()}
	task := imageTask(t, "https://e.com/img/noext")

	// No Content-Type and no URL extension leaves nothing to derive an extension from.
	_, _, err := ip.saveImageToDisk(task, imageResponse("data", "", ""), 0, dir)
	if err == nil {
		t.Fatal("expected a filename-derivation error")
	}
	if !strings.Contains(err.Error(), "extension") {
		t.Errorf("error = %v, want an extension error", err)
	}
}

// stubFetcher serves canned responses keyed by URL.
type stubFetcher struct {
	mu        sync.Mutex
	responses map[string]*http.Response
	err       error
	calls     []string
}

func (s *stubFetcher) FetchWithRetry(req *http.Request, _ context.Context) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, req.URL.String())
	if s.err != nil {
		return nil, s.err
	}
	if resp, ok := s.responses[req.URL.String()]; ok {
		return resp, nil
	}
	return nil, fmt.Errorf("no stub response for %s", req.URL.String())
}

func (s *stubFetcher) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func TestFetchImageData_RejectsOversizeContentLengthHeader(t *testing.T) {
	raw := "https://e.com/img/big.png"
	ip := &ImageProcessor{
		fetcher: &stubFetcher{responses: map[string]*http.Response{
			raw: imageResponse(strings.Repeat("x", 100), "image/png", "100"),
		}},
		log: silentLog(),
	}

	_, err := ip.fetchImageData(imageTask(t, raw), "ua", 10)
	if err == nil {
		t.Fatal("expected a header size rejection")
	}
	if !strings.Contains(err.Error(), "exceeds max size based on header") {
		t.Errorf("error = %v, want a header-based size rejection", err)
	}
}

func TestFetchImageData_WrapsFetchError(t *testing.T) {
	ip := &ImageProcessor{
		fetcher: &stubFetcher{err: errors.New("network down")},
		log:     silentLog(),
	}

	_, err := ip.fetchImageData(imageTask(t, "https://e.com/img/x.png"), "ua", 0)
	if err == nil {
		t.Fatal("expected a fetch error")
	}
	if !strings.Contains(err.Error(), "fetch failed for img") {
		t.Errorf("error = %v, want a wrapped fetch failure", err)
	}
}

func TestFetchImageData_AllowsResponseWithinLimit(t *testing.T) {
	raw := "https://e.com/img/ok.png"
	ip := &ImageProcessor{
		fetcher: &stubFetcher{responses: map[string]*http.Response{
			raw: imageResponse("data", "image/png", "4"),
		}},
		log: silentLog(),
	}

	resp, err := ip.fetchImageData(imageTask(t, raw), "ua", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// fakeImageStore records image DB reads and writes.
type fakeImageStore struct {
	mu       sync.Mutex
	entries  map[string]*models.ImageDBEntry
	status   map[string]models.ImageStatus
	checkErr error
	updates  map[string]*models.ImageDBEntry
}

func newFakeImageStore() *fakeImageStore {
	return &fakeImageStore{
		entries: map[string]*models.ImageDBEntry{},
		status:  map[string]models.ImageStatus{},
		updates: map[string]*models.ImageDBEntry{},
	}
}

func (f *fakeImageStore) CheckImageStatus(u string) (models.ImageStatus, *models.ImageDBEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.checkErr != nil {
		return models.ImageStatusDBError, nil, f.checkErr
	}
	st, ok := f.status[u]
	if !ok {
		return models.ImageStatusNotFound, nil, nil
	}
	return st, f.entries[u], nil
}

func (f *fakeImageStore) UpdateImageStatus(u string, e *models.ImageDBEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updates[u] = e
	return nil
}

// newTestImageProcessor wires an ImageProcessor whose robots handler always
// fails open (its fetcher errors), so robots never blocks a test image.
func newTestImageProcessor(t *testing.T, store *fakeImageStore, imgFetcher fetch.HTTPFetcher, resolved *config.ResolvedSiteConfig, siteCfg *config.SiteConfig) *ImageProcessor {
	t.Helper()

	appCfg := &config.AppConfig{
		NumWorkers:         2,
		NumImageWorkers:    2,
		MaxRequests:        4,
		MaxRequestsPerHost: 2,
	}
	rl := fetch.NewRateLimiter(0, silentLog())
	globalSem := semaphore.NewWeighted(int64(appCfg.MaxRequests))
	hostSem := fetch.NewHostSemaphorePool(appCfg.MaxRequestsPerHost, silentLog())
	robots := fetch.NewRobotsHandler(&stubFetcher{err: errors.New("no robots")}, rl, globalSem, hostSem, nil, appCfg, silentLog())

	return NewImageProcessor(store, imgFetcher, robots, rl, globalSem, hostSem, resolved, appCfg, silentLog())
}

func imageDoc(t *testing.T, html string) *goquery.Selection {
	t.Helper()
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		t.Fatalf("parse html: %v", err)
	}
	return doc.Selection
}

func crawlStatuses(sel *goquery.Selection) []string {
	var out []string
	sel.Find("img").Each(func(_ int, s *goquery.Selection) {
		v, _ := s.Attr("data-crawl-status")
		out = append(out, v)
	})
	return out
}

func TestProcessImages_SkipImagesShortCircuits(t *testing.T) {
	store := newFakeImageStore()
	fetcher := &stubFetcher{}
	ip := newTestImageProcessor(t, store, fetcher, &config.ResolvedSiteConfig{SkipImages: true}, &config.SiteConfig{})

	sel := imageDoc(t, `<body><img src="/a.png"><img src="/b.png"></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	imgMap, errs := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

	if len(imgMap) != 0 || len(errs) != 0 {
		t.Errorf("expected no images and no errors, got %d images %d errors", len(imgMap), len(errs))
	}
	if fetcher.callCount() != 0 {
		t.Errorf("fetcher was called %d times despite skip_images", fetcher.callCount())
	}
	for _, s := range crawlStatuses(sel) {
		if s != "skipped-config" {
			t.Errorf("data-crawl-status = %q, want skipped-config", s)
		}
	}
}

func TestProcessImages_MarksNonDownloadableSources(t *testing.T) {
	tests := []struct {
		name       string
		html       string
		wantStatus string
	}{
		{"EmptySrc", `<img src="">`, "skipped-empty-src"},
		{"DataURI", `<img src="data:image/png;base64,AAAA">`, "skipped-data-uri"},
		{"NonHTTPScheme", `<img src="ftp://e.com/a.png">`, "skipped-scheme"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeImageStore()
			fetcher := &stubFetcher{}
			ip := newTestImageProcessor(t, store, fetcher, &config.ResolvedSiteConfig{}, &config.SiteConfig{})

			sel := imageDoc(t, "<body>"+tt.html+"</body>")
			pageURL, _ := url.Parse("https://e.com/page.html")

			ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

			got := crawlStatuses(sel)
			if len(got) != 1 || got[0] != tt.wantStatus {
				t.Errorf("data-crawl-status = %v, want [%s]", got, tt.wantStatus)
			}
			if fetcher.callCount() != 0 {
				t.Errorf("fetcher called %d times, want 0", fetcher.callCount())
			}
		})
	}
}

func TestProcessImages_DomainFilter(t *testing.T) {
	store := newFakeImageStore()
	fetcher := &stubFetcher{}
	siteCfg := &config.SiteConfig{DisallowedImageDomains: []string{"ads.example.com"}}
	ip := newTestImageProcessor(t, store, fetcher, &config.ResolvedSiteConfig{}, siteCfg)

	sel := imageDoc(t, `<body><img src="https://ads.example.com/track.png"></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	ip.ProcessImages(sel, pageURL, siteCfg, t.TempDir(), silentLog(), context.Background())

	got := crawlStatuses(sel)
	if len(got) != 1 || got[0] != "skipped-domain" {
		t.Errorf("data-crawl-status = %v, want [skipped-domain]", got)
	}
}

func TestProcessImages_ReusesAlreadyDownloadedImage(t *testing.T) {
	raw := "https://e.com/img/cached.png"

	store := newFakeImageStore()
	store.status[raw] = models.ImageStatusSuccess
	store.entries[raw] = &models.ImageDBEntry{
		Status:    models.ImageStatusSuccess,
		LocalPath: "images/cached_abc123.png",
		Caption:   "cached caption",
	}

	fetcher := &stubFetcher{}
	ip := newTestImageProcessor(t, store, fetcher, &config.ResolvedSiteConfig{}, &config.SiteConfig{})

	sel := imageDoc(t, `<body><img src="`+raw+`" alt="ignored"></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	imgMap, errs := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if fetcher.callCount() != 0 {
		t.Errorf("re-fetched an image already marked success (%d calls)", fetcher.callCount())
	}
	data, ok := imgMap[raw]
	if !ok {
		t.Fatalf("image %q missing from result map %v", raw, imgMap)
	}
	if data.LocalPath != "images/cached_abc123.png" {
		t.Errorf("LocalPath = %q, want the cached path", data.LocalPath)
	}
	if got := crawlStatuses(sel); len(got) != 1 || got[0] != "success" {
		t.Errorf("data-crawl-status = %v, want [success]", got)
	}
}

func TestProcessImages_DownloadsAndRecordsImage(t *testing.T) {
	raw := "https://e.com/img/new.png"
	dir := t.TempDir()

	store := newFakeImageStore()
	fetcher := &stubFetcher{responses: map[string]*http.Response{
		raw: imageResponse("PNGBYTES", "image/png", ""),
	}}
	ip := newTestImageProcessor(t, store, fetcher, &config.ResolvedSiteConfig{}, &config.SiteConfig{})

	sel := imageDoc(t, `<body><img src="`+raw+`" alt="a picture"></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	imgMap, errs := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, dir, silentLog(), context.Background())

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	data, ok := imgMap[raw]
	if !ok {
		t.Fatalf("image %q missing from result map %v", raw, imgMap)
	}
	if data.Caption != "a picture" {
		t.Errorf("Caption = %q, want the alt text", data.Caption)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(data.LocalPath))); err != nil {
		t.Errorf("downloaded file not on disk: %v", err)
	}
	if entry := store.updates[raw]; entry == nil || entry.Status != models.ImageStatusSuccess {
		t.Errorf("store update = %+v, want a success entry", entry)
	}
}

func TestProcessImages_CaptionPrefersFigcaptionOverAlt(t *testing.T) {
	raw := "https://e.com/img/fig.png"

	store := newFakeImageStore()
	store.status[raw] = models.ImageStatusSuccess
	store.entries[raw] = &models.ImageDBEntry{Status: models.ImageStatusSuccess, LocalPath: "images/fig.png"}

	ip := newTestImageProcessor(t, store, &stubFetcher{}, &config.ResolvedSiteConfig{}, &config.SiteConfig{})

	sel := imageDoc(t, `<body><figure>
		<img src="`+raw+`" alt="alt text">
		<figcaption> the real caption </figcaption>
	</figure></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	imgMap, _ := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

	if imgMap[raw].Caption != "the real caption" {
		t.Errorf("Caption = %q, want the trimmed figcaption text", imgMap[raw].Caption)
	}
}

func TestProcessImages_ReportsDBCheckError(t *testing.T) {
	store := newFakeImageStore()
	store.checkErr = errors.New("db exploded")

	ip := newTestImageProcessor(t, store, &stubFetcher{}, &config.ResolvedSiteConfig{}, &config.SiteConfig{})

	sel := imageDoc(t, `<body><img src="https://e.com/img/x.png"></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	_, errs := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

	if len(errs) == 0 {
		t.Fatal("expected the DB error to be reported")
	}
	if got := crawlStatuses(sel); len(got) != 1 || got[0] != "error-db" {
		t.Errorf("data-crawl-status = %v, want [error-db]", got)
	}
}

// concurrencyFetcher serves a body for any URL while recording how many
// downloads were in flight at once.
type concurrencyFetcher struct {
	mu       sync.Mutex
	inFlight int
	maxSeen  int
	calls    int
}

func (c *concurrencyFetcher) FetchWithRetry(req *http.Request, _ context.Context) (*http.Response, error) {
	c.mu.Lock()
	c.inFlight++
	c.calls++
	if c.inFlight > c.maxSeen {
		c.maxSeen = c.inFlight
	}
	c.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	c.mu.Lock()
	c.inFlight--
	c.mu.Unlock()

	return imageResponse("DATA", "image/png", ""), nil
}

func newImageProcessorWithWorkers(t *testing.T, store *fakeImageStore, fetcher fetch.HTTPFetcher, workers int) *ImageProcessor {
	t.Helper()

	appCfg := &config.AppConfig{
		NumWorkers:         workers,
		NumImageWorkers:    workers,
		MaxRequests:        32,
		MaxRequestsPerHost: 32,
	}
	rl := fetch.NewRateLimiter(0, silentLog())
	globalSem := semaphore.NewWeighted(int64(appCfg.MaxRequests))
	hostSem := fetch.NewHostSemaphorePool(appCfg.MaxRequestsPerHost, silentLog())
	robots := fetch.NewRobotsHandler(&stubFetcher{err: errors.New("no robots")}, rl, globalSem, hostSem, nil, appCfg, silentLog())

	return NewImageProcessor(store, fetcher, robots, rl, globalSem, hostSem, &config.ResolvedSiteConfig{}, appCfg, silentLog())
}

func imagesHTML(n int) string {
	var b strings.Builder
	b.WriteString("<body>")
	for i := range n {
		fmt.Fprintf(&b, `<img src="https://e.com/img/%d.png">`, i)
	}
	b.WriteString("</body>")
	return b.String()
}

func TestProcessImages_DownloadsEveryImageOnThePage(t *testing.T) {
	const count = 6
	dir := t.TempDir()

	store := newFakeImageStore()
	fetcher := &concurrencyFetcher{}
	ip := newImageProcessorWithWorkers(t, store, fetcher, 3)

	sel := imageDoc(t, imagesHTML(count))
	pageURL, _ := url.Parse("https://e.com/page.html")

	imgMap, errs := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, dir, silentLog(), context.Background())

	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(imgMap) != count {
		t.Errorf("downloaded %d images, want %d", len(imgMap), count)
	}
	if fetcher.calls != count {
		t.Errorf("fetcher called %d times, want %d", fetcher.calls, count)
	}
	for _, data := range imgMap {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(data.LocalPath))); err != nil {
			t.Errorf("image %q not written to disk: %v", data.OriginalURL, err)
		}
	}
}

// num_image_workers must still cap how many downloads run at once.
func TestProcessImages_BoundsConcurrentDownloads(t *testing.T) {
	const workers = 2

	store := newFakeImageStore()
	fetcher := &concurrencyFetcher{}
	ip := newImageProcessorWithWorkers(t, store, fetcher, workers)

	sel := imageDoc(t, imagesHTML(8))
	pageURL, _ := url.Parse("https://e.com/page.html")

	ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	if fetcher.maxSeen > workers {
		t.Errorf("observed %d concurrent downloads, want at most %d", fetcher.maxSeen, workers)
	}
	if fetcher.maxSeen < 2 {
		t.Errorf("observed %d concurrent downloads, expected the work to actually overlap", fetcher.maxSeen)
	}
}

func TestProcessImages_CancelledContextStopsDispatch(t *testing.T) {
	store := newFakeImageStore()
	fetcher := &concurrencyFetcher{}
	ip := newImageProcessorWithWorkers(t, store, fetcher, 2)

	sel := imageDoc(t, imagesHTML(3))
	pageURL, _ := url.Parse("https://e.com/page.html")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	imgMap, _ := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), ctx)

	if len(imgMap) != 0 {
		t.Errorf("downloaded %d images despite a cancelled context", len(imgMap))
	}
	if fetcher.calls != 0 {
		t.Errorf("fetcher called %d times despite a cancelled context", fetcher.calls)
	}
	for _, s := range crawlStatuses(sel) {
		if s != "error-dispatch-context" {
			t.Errorf("data-crawl-status = %q, want error-dispatch-context", s)
		}
	}
}

func TestProcessImages_FailedDownloadRecordsFailureEntry(t *testing.T) {
	raw := "https://e.com/img/broken.png"

	store := newFakeImageStore()
	fetcher := &stubFetcher{err: errors.New("connection reset")}
	ip := newTestImageProcessor(t, store, fetcher, &config.ResolvedSiteConfig{}, &config.SiteConfig{})

	sel := imageDoc(t, `<body><img src="`+raw+`"></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	imgMap, errs := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

	if len(errs) == 0 {
		t.Fatal("expected a download error to be reported")
	}
	if len(imgMap) != 0 {
		t.Errorf("imageMap = %v, want empty on failure", imgMap)
	}
	entry := store.updates[raw]
	if entry == nil || entry.Status != models.ImageStatusFailure {
		t.Fatalf("store update = %+v, want a failure entry", entry)
	}
	// ErrorType now carries the underlying message rather than a category constant.
	if !strings.Contains(entry.ErrorType, "connection reset") {
		t.Errorf("ErrorType = %q, want it to carry the underlying error text", entry.ErrorType)
	}
}
