package crawler

import "fmt"

const (
	RefusedEmpty             = "empty"
	RefusedTemporaryFailures = "temporary_failures"
	RefusedShrink            = "shrink"
)

// Measured on 17 healthy re-crawls of real docs sites: corpus sizes never
// changed and no page failed transiently (docs/research/v2.25.0-lossy-crawl.md).
const (
	shrinkMinBaseline    = 20
	shrinkMaxKeptPercent = 70
	transientMinCount    = 3
	transientMaxPercent  = 25
)

type lossInput struct {
	Baseline    int
	HasBaseline bool
	NewPages    int
	Carryable   int // failed temporarily; published as their previous copy
	Attempted   int
	Transient   int
	Incremental bool
	Processed   int64
	Succeeded   int64
	Unchanged   int64
	AllowShrink bool
}

type lossVerdict struct {
	Refused   bool
	Reason    string
	NewPages  int
	Baseline  int
	Transient int
	Attempted int
	Processed int64
}

func decideLoss(in lossInput) lossVerdict {
	kept := in.NewPages + in.Carryable
	v := lossVerdict{NewPages: kept, Baseline: in.Baseline, Transient: in.Transient, Attempted: in.Attempted, Processed: in.Processed}
	refuse := func(reason string) lossVerdict {
		v.Refused, v.Reason = true, reason
		return v
	}
	if in.Processed > 0 {
		if in.NewPages == 0 {
			return refuse(RefusedEmpty)
		}
		if in.Incremental && in.Succeeded == 0 && in.Unchanged == 0 {
			return refuse(RefusedEmpty)
		}
	}
	if in.AllowShrink {
		return v
	}
	if in.Transient >= transientMinCount && in.Transient*100 >= in.Attempted*transientMaxPercent {
		return refuse(RefusedTemporaryFailures)
	}
	if in.HasBaseline && in.Baseline >= shrinkMinBaseline && kept*100 <= in.Baseline*shrinkMaxKeptPercent {
		return refuse(RefusedShrink)
	}
	return v
}

// LossyCrawlError reports a crawl that finished but was not published because
// it lost too much. The previous corpus is untouched; StagingKept says whether
// a staged crawl is waiting for --resume.
type LossyCrawlError struct {
	SiteKey     string
	Reason      string
	NewPages    int
	Baseline    int
	Transient   int
	Attempted   int
	Processed   int64
	Failures    Failures
	StagingKept bool
}

func (e *LossyCrawlError) Error() string {
	switch e.Reason {
	case RefusedEmpty:
		return fmt.Sprintf("crawl kept back: zero successful pages (%d attempted)", e.Processed)
	case RefusedTemporaryFailures:
		msg := fmt.Sprintf("crawl kept back: %d of %d pages failed with temporary errors", e.Transient, e.Attempted)
		for _, c := range e.Failures.Classes {
			if c.Class == FailureTemporary {
				msg += fmt.Sprintf(" (e.g. %s: %s)", c.SampleURL, c.SampleError)
				break
			}
		}
		return msg
	case RefusedShrink:
		return fmt.Sprintf("crawl kept back: this crawl has %d pages, the last recorded crawl had %d (-%d%%)",
			e.NewPages, e.Baseline, (e.Baseline-e.NewPages)*100/max(e.Baseline, 1))
	default:
		return "crawl kept back: " + e.Reason
	}
}

// Hint is the next step for surface "cli" or "mcp".
func (e *LossyCrawlError) Hint(surface string) string {
	canAccept := e.Reason != RefusedEmpty
	if !e.StagingKept {
		if surface == "mcp" {
			if canAccept {
				return "The existing files were kept and this run is not recorded in crawl history. Call crawl_site again later, or with allow_shrink:true to accept it."
			}
			return "The existing files were kept and this run is not recorded in crawl history. Call crawl_site again later."
		}
		if canAccept {
			return "The existing files were kept and this run is not recorded in crawl history. Try again later, or pass -allow-shrink to accept it."
		}
		return "The existing files were kept and this run is not recorded in crawl history. Try again later."
	}
	if surface == "mcp" {
		if canAccept {
			return "The previous corpus was kept. Call crawl_site with incremental:true to retry the failed pages and continue the kept crawl, or with incremental:true and allow_shrink:true to publish it as it is."
		}
		return "The previous corpus was kept. Call crawl_site with incremental:true to retry the failed pages and continue the kept crawl."
	}
	hint := fmt.Sprintf("The previous corpus is untouched. Retry the failed pages with `doc-scraper crawl -site %s --resume`", e.SiteKey)
	if canAccept {
		hint += fmt.Sprintf(", or publish this crawl as it is with `doc-scraper crawl -site %s --resume -allow-shrink`", e.SiteKey)
	}
	return hint + "."
}
