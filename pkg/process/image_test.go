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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
			assert.Equal(t, tt.want, matchDomain(tt.host, tt.pattern), "matchDomain(%q, %q)", tt.host, tt.pattern)
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
			assert.Equal(t, tt.want, isDomainAllowed(tt.host, tt.allowed, tt.disallowed), "isDomainAllowed(%q, %v, %v)", tt.host, tt.allowed, tt.disallowed)
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
		{"BackslashURLExtensionRejected", "https://e.com/img/pic.png%5C..%5C..%5Cevil", "", "", "", true},
		{"StreamURLExtensionRejected", "https://e.com/img/pic.png:evil", "!!!", "", "", true},
		{"UnsafeURLExtensionReplacedByContentType", "https://e.com/img/pic.png:evil", "image/png", "pic_", ".png", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.rawURL)
			require.NoError(t, err, "parse")
			got, err := generateLocalFilename(u, tt.rawURL, tt.contentType, silentLog())
			if tt.wantErr {
				require.Error(t, err, "expected error, got filename %q", got)
				return
			}
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(got, tt.wantPrefix), "filename %q, want prefix %q", got, tt.wantPrefix)
			assert.True(t, strings.HasSuffix(got, tt.wantExt), "filename %q, want extension %q", got, tt.wantExt)
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
	require.NoError(t, err)
	nameB, err := generateLocalFilename(ub, b, "image/png", silentLog())
	require.NoError(t, err)
	assert.NotEqual(t, nameA, nameB, "distinct URLs produced the same filename")
	assert.True(t, strings.HasPrefix(nameA, "logo_") && strings.HasPrefix(nameB, "logo_"), "expected both to keep the logo_ base: %q %q", nameA, nameB)
}

// A URL with no usable base name still yields a unique filename: SanitizeFilename
// substitutes "untitled" and the URL hash suffix keeps the names distinct.
func TestGenerateLocalFilename_DegenerateBaseNamesStayUnique(t *testing.T) {
	rawA := "https://e.com/"
	rawB := "https://other.com/"

	ua, _ := url.Parse(rawA)
	ub, _ := url.Parse(rawB)

	gotA, err := generateLocalFilename(ua, rawA, "image/png", silentLog())
	require.NoError(t, err)
	gotB, err := generateLocalFilename(ub, rawB, "image/png", silentLog())
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(gotA, "untitled_"), "filename %q, want the untitled_ base", gotA)
	assert.NotEqual(t, gotA, gotB, "distinct degenerate URLs produced the same filename")
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
	require.NoError(t, err, "parse")
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
	require.NoError(t, err)
	assert.Equal(t, int64(len("PNGDATA")), n, "copied bytes")
	assert.True(t, strings.HasPrefix(rel, ImageDir+"/"), "relative path %q, want it under %q with forward slashes", rel, ImageDir)
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	require.NoError(t, err, "read written file")
	assert.Equal(t, "PNGDATA", string(data), "file contents")
}

func TestSaveImageToDisk_RejectsAndRemovesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	ip := &ImageProcessor{log: silentLog()}
	task := imageTask(t, "https://e.com/img/big.png")

	// 20 bytes of body against a 10-byte cap, with no Content-Length to vouch for it.
	_, _, err := ip.saveImageToDisk(task, imageResponse(strings.Repeat("x", 20), "image/png", ""), 10, dir)
	require.Error(t, err, "expected an oversize error")
	require.ErrorContains(t, err, "exceeds max size")

	entries, readErr := os.ReadDir(filepath.Join(dir, ImageDir))
	require.NoError(t, readErr, "read image dir")
	assert.Empty(t, entries, "truncated file was left behind")
}

// A file exactly at the limit is kept when Content-Length confirms it was complete.
func TestSaveImageToDisk_KeepsFileExactlyAtLimitWhenContentLengthAgrees(t *testing.T) {
	dir := t.TempDir()
	ip := &ImageProcessor{log: silentLog()}
	task := imageTask(t, "https://e.com/img/exact.png")

	body := strings.Repeat("x", 10)
	rel, n, err := ip.saveImageToDisk(task, imageResponse(body, "image/png", "10"), 10, dir)
	require.NoError(t, err)
	assert.Equal(t, int64(10), n, "copied bytes")
	assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(rel)), "file at exactly the limit should have been kept")
}

func TestSaveImageToDisk_PropagatesFilenameError(t *testing.T) {
	dir := t.TempDir()
	ip := &ImageProcessor{log: silentLog()}
	task := imageTask(t, "https://e.com/img/noext")

	// No Content-Type and no URL extension leaves nothing to derive an extension from.
	_, _, err := ip.saveImageToDisk(task, imageResponse("data", "", ""), 0, dir)
	require.Error(t, err, "expected a filename-derivation error")
	require.ErrorContains(t, err, "extension")
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
	require.Error(t, err, "expected a header size rejection")
	require.ErrorContains(t, err, "exceeds max size based on header")
}

func TestFetchImageData_WrapsFetchError(t *testing.T) {
	ip := &ImageProcessor{
		fetcher: &stubFetcher{err: errors.New("network down")},
		log:     silentLog(),
	}

	_, err := ip.fetchImageData(imageTask(t, "https://e.com/img/x.png"), "ua", 0)
	require.Error(t, err, "expected a fetch error")
	require.ErrorContains(t, err, "fetch failed for img")
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
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
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
	require.NoError(t, err, "parse html")
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

	assert.Empty(t, imgMap)
	assert.Empty(t, errs)
	assert.Equal(t, 0, fetcher.callCount(), "fetcher was called despite skip_images")
	for _, s := range crawlStatuses(sel) {
		assert.Equal(t, "skipped-config", s, "data-crawl-status")
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

			assert.Equal(t, []string{tt.wantStatus}, crawlStatuses(sel), "data-crawl-status")
			assert.Equal(t, 0, fetcher.callCount(), "fetcher calls")
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

	assert.Equal(t, []string{"skipped-domain"}, crawlStatuses(sel), "data-crawl-status")
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

	require.Empty(t, errs)
	assert.Equal(t, 0, fetcher.callCount(), "re-fetched an image already marked success")
	require.Contains(t, imgMap, raw)
	assert.Equal(t, "images/cached_abc123.png", imgMap[raw].LocalPath, "LocalPath should be the cached path")
	assert.Equal(t, []string{"success"}, crawlStatuses(sel), "data-crawl-status")
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

	require.Empty(t, errs)
	require.Contains(t, imgMap, raw)
	data := imgMap[raw]
	assert.Equal(t, "a picture", data.Caption, "Caption should be the alt text")
	assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(data.LocalPath)), "downloaded file not on disk")
	if assert.NotNil(t, store.updates[raw], "store update") {
		assert.Equal(t, models.ImageStatusSuccess, store.updates[raw].Status)
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

	assert.Equal(t, "the real caption", imgMap[raw].Caption, "Caption should be the trimmed figcaption text")
}

func TestProcessImages_ReportsDBCheckError(t *testing.T) {
	store := newFakeImageStore()
	store.checkErr = errors.New("db exploded")

	ip := newTestImageProcessor(t, store, &stubFetcher{}, &config.ResolvedSiteConfig{}, &config.SiteConfig{})

	sel := imageDoc(t, `<body><img src="https://e.com/img/x.png"></body>`)
	pageURL, _ := url.Parse("https://e.com/page.html")

	_, errs := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), context.Background())

	require.NotEmpty(t, errs, "expected the DB error to be reported")
	assert.Equal(t, []string{"error-db"}, crawlStatuses(sel), "data-crawl-status")
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

	require.Empty(t, errs)
	assert.Len(t, imgMap, count, "downloaded images")
	assert.Equal(t, count, fetcher.calls, "fetcher calls")
	for _, data := range imgMap {
		assert.FileExists(t, filepath.Join(dir, filepath.FromSlash(data.LocalPath)), "image %q not written to disk", data.OriginalURL)
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
	assert.LessOrEqual(t, fetcher.maxSeen, workers, "concurrent downloads")
	assert.GreaterOrEqual(t, fetcher.maxSeen, 2, "expected the work to actually overlap")
}

// num_image_workers falls back to num_workers only when unset, so each value
// must be observable on its own.
func TestProcessImages_WorkerCountSource(t *testing.T) {
	tests := []struct {
		name         string
		imageWorkers int
		workers      int
		wantMax      int
	}{
		{"ExplicitImageWorkers", 3, 1, 3},
		{"UnsetFallsBackToWorkers", 0, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := &concurrencyFetcher{}
			ip := newImageProcessorWithWorkers(t, newFakeImageStore(), fetcher, tt.workers)
			ip.appCfg.NumImageWorkers = tt.imageWorkers

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			sel := imageDoc(t, imagesHTML(9))
			pageURL, _ := url.Parse("https://e.com/page.html")
			imgMap, _ := ip.ProcessImages(sel, pageURL, &config.SiteConfig{}, t.TempDir(), silentLog(), ctx)

			assert.Len(t, imgMap, 9, "every image should download")
			fetcher.mu.Lock()
			defer fetcher.mu.Unlock()
			assert.Equal(t, tt.wantMax, fetcher.maxSeen, "peak concurrent downloads")
		})
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

	assert.Empty(t, imgMap, "downloaded images despite a cancelled context")
	assert.Equal(t, 0, fetcher.calls, "fetcher called despite a cancelled context")
	for _, s := range crawlStatuses(sel) {
		assert.Equal(t, "error-dispatch-context", s, "data-crawl-status")
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

	require.NotEmpty(t, errs, "expected a download error to be reported")
	assert.Empty(t, imgMap, "imageMap should be empty on failure")
	entry := store.updates[raw]
	require.NotNil(t, entry, "store update")
	require.Equal(t, models.ImageStatusFailure, entry.Status)
	// ErrorType now carries the underlying message rather than a category constant.
	assert.Contains(t, entry.ErrorType, "connection reset", "ErrorType should carry the underlying error text")
}
