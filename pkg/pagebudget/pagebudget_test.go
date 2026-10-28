package pagebudget

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// sixUnits weighs 6: six tag tokens, no attributes, 21 bytes.
var sixUnits = []byte(strings.Repeat("<p></p>", 3))

func TestWeigh(t *testing.T) {
	assert.EqualValues(t, 3, weigh([]byte(`<p a b c>x</p>`)), "2 tags + 3 attrs/3 + 14 bytes/64")
	assert.EqualValues(t, 6, weigh(sixUnits))
	assert.EqualValues(t, 2+100, weigh([]byte("<p>"+strings.Repeat("a", 64*100-7)+"</p>")), "byte term")
	assert.EqualValues(t, 1+1+2, weigh([]byte(`<br/><img a b c d e f>`)), "self-closing and start tags count, 6 attrs")
}

// tagAttrCounts counts attributes per start tag through TagAttr, the
// reference countAttrs must agree with.
func tagAttrCounts(body []byte) (got, want []int64) {
	z := html.NewTokenizer(bytes.NewReader(body))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return got, want
		case html.StartTagToken, html.SelfClosingTagToken:
			got = append(got, countAttrs(z.Raw()))
			var n int64
			for _, more := z.TagName(); more; {
				_, _, more = z.TagAttr()
				n++
			}
			want = append(want, n)
		}
	}
}

func TestCountAttrs_MatchesTokenizer(t *testing.T) {
	for _, in := range []string{
		`<p>`,
		`<p a b c>`,
		`<br/>`,
		`<img a b c d e f>`,
		`<a href="x>y" title='q"r'>t</a>`,
		`<a href=x>`,
		`<a href=x/>`,
		`<a =x b>`,
		`<p a=>`,
		`<p a = "b" c>`,
		`<p/a/b>`,
		`<p / a>`,
		`<input disabled/>`,
		"<p\na\tb\fc\rd>",
		`<div class=a id=b data-x="1" data-y='2' hidden>`,
		`<script src=x async></script>`,
		`<p a="b`,
		`<p a='b c>d' e`,
	} {
		got, want := tagAttrCounts([]byte(in))
		assert.Equal(t, want, got, "%q", in)
	}
}

func TestCountAttrs_KeepsDuplicates(t *testing.T) {
	assert.EqualValues(t, 3, countAttrs([]byte(`<p a a A>`)), "the tokenizer keeps one; counting all only raises the weight")
}

func TestCountAttrs_DoesNotAllocate(t *testing.T) {
	raw := []byte(`<div class="a b" id=x data-y='z' hidden>`)
	assert.Zero(t, testing.AllocsPerRun(100, func() { countAttrs(raw) }))
}

func TestParse_UnderCap(t *testing.T) {
	doc, release, err := NewBudget(100).Parse(context.Background(), []byte(`<html><body><p>hi</p></body></html>`))
	require.NoError(t, err)
	defer release()
	assert.Equal(t, "hi", doc.Find("p").Text())
}

func TestParse_OverBudgetCapacityRefusedWithoutWaiting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err := NewBudget(5).Parse(ctx, sixUnits)
	require.ErrorIs(t, err, ErrPageTooComplex)
	assert.NoError(t, ctx.Err(), "must fail immediately, not wait")
}

func TestParse_OverPageCapRefused(t *testing.T) {
	dense := []byte(strings.Repeat("<p>a</p>", MaxPageUnits/2+1))
	_, _, err := NewBudget(4*MaxPageUnits).Parse(context.Background(), dense)
	assert.ErrorIs(t, err, ErrPageTooComplex)
}

func TestParse_ByteWeightCounts(t *testing.T) {
	_, _, err := NewBudget(50).Parse(context.Background(), []byte("<p>"+strings.Repeat("a", 64*100)+"</p>"))
	assert.ErrorIs(t, err, ErrPageTooComplex, "two tags but 6.4 KB of text weighs about 102")
}

func TestParse_WaitsForRelease(t *testing.T) {
	b := NewBudget(10)
	_, release, err := b.Parse(context.Background(), sixUnits)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		_, r, err := b.Parse(context.Background(), sixUnits)
		if err == nil {
			r()
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("second parse ran while the first held the budget")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("second parse did not run after release")
	}
}

func TestParse_CancelledWhileWaiting(t *testing.T) {
	b := NewBudget(10)
	_, release, err := b.Parse(context.Background(), sixUnits)
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, _, err = b.Parse(ctx, sixUnits)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestParse_DoubleReleaseIsHarmless(t *testing.T) {
	b := NewBudget(6)
	_, release, err := b.Parse(context.Background(), sixUnits)
	require.NoError(t, err)
	release()
	release()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, r, err := b.Parse(ctx, sixUnits)
	require.NoError(t, err, "the full capacity is available again")
	r()
}

func TestParse_ParseErrorReleases(t *testing.T) {
	b := NewBudget(1000)
	_, _, err := b.Parse(context.Background(), []byte(strings.Repeat("<div>", 600)))
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrPageTooComplex)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Weighs 843, more than the 354 left if the failed parse (646) kept its units.
	_, r, err := b.Parse(ctx, []byte(strings.Repeat("<p></p>", 400)))
	require.NoError(t, err, "a failed parse gives its units back")
	r()
}

func TestReserve(t *testing.T) {
	parent := NewBudget(10)
	child, releaseAll, err := parent.Reserve(context.Background(), 8)
	require.NoError(t, err)

	_, _, err = child.Parse(context.Background(), sixUnits)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = child.Parse(ctx, sixUnits)
	require.ErrorIs(t, err, ErrPageTooComplex, "the child never waits")
	require.NoError(t, ctx.Err())

	done := make(chan error, 1)
	go func() {
		_, r, err := parent.Parse(context.Background(), sixUnits)
		if err == nil {
			r()
		}
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("parent had room while the reservation was held")
	case <-time.After(50 * time.Millisecond):
	}
	releaseAll()
	releaseAll()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("parent did not get the reservation back")
	}
}

func TestReserve_OverCapacity(t *testing.T) {
	_, _, err := NewBudget(10).Reserve(context.Background(), 11)
	assert.ErrorIs(t, err, ErrPageTooComplex)
}
