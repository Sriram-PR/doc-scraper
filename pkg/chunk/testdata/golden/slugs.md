# Heading text and anchors

Each heading below carries inline Markdown; its anchor and heading path use the rendered text.

## Foo [bar](http://example.com/x)

A link keeps its text and drops its URL.

## [2.0.0](https://example.com/compare/v1.1.2...v2.0.0) - 2026-06-07

A keep-a-changelog release heading; GitHub's id for this is 200---2026-06-07.

## Use `code()` and *em* **b**

Code span content stays, emphasis markers go.

## ![logo](https://example.com/logo.png) Logo here

An image adds nothing.

## Title <span>x</span> HTML

Inline HTML tags drop, their text stays.

## Fish &amp; Chips

Entities decode before slugging.

## Escaped \*stars\*

Backslash escapes resolve to the literal character.

## Autolink <https://example.com>

An autolink's text is its URL.
