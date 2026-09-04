package detect

// FrameworkSignature describes how to recognize one documentation framework
// and where its main content lives. Signals are tiered by trustworthiness:
// generator meta values are machine-set and trusted alone, DOM selectors are
// structural evidence, asset substrings are weak corroboration. GenGates list
// generic generator values (Next.js, Jekyll, Hugo, Astro) that add a little
// confidence but never confirm on their own.
type FrameworkSignature struct {
	Framework   Framework
	GenContains []string
	GenGates    []string
	DOMAny      []string
	AssetSubs   []string
	AssetVeto   []string
	Selector    string
	Exclude     string
}

// frameworkSignatures is ordered most-specific first: themed variants must
// precede their generic family (furo/pydata/book before sphinx, material/rtd
// before plain mkdocs) so equal scores resolve to the specific entry.
var frameworkSignatures = []FrameworkSignature{
	{
		Framework:   FrameworkDocusaurus,
		GenContains: []string{"docusaurus"},
		DOMAny:      []string{"[data-docusaurus]", ".theme-doc-markdown"},
		AssetSubs:   []string{"/assets/js/runtime~main."},
		Selector:    "article .theme-doc-markdown, .theme-doc-markdown, main article",
	},
	{
		Framework:   FrameworkVitePress,
		GenContains: []string{"vitepress"},
		DOMAny:      []string{".vp-doc", "#VPContent"},
		Selector:    ".vp-doc, main.main, #VPContent",
	},
	{
		Framework:   FrameworkVuePress,
		GenContains: []string{"vuepress"},
		DOMAny:      []string{"[vp-content]"},
		Selector:    "[vp-content] #content, .theme-hope-content, [vp-content]",
	},
	{
		Framework:   FrameworkStarlight,
		GenContains: []string{"starlight"},
		GenGates:    []string{"astro"},
		DOMAny:      []string{".sl-markdown-content", "starlight-toc", "#starlight__sidebar"},
		AssetSubs:   []string{"/_astro/"},
		Selector:    "main[data-pagefind-body] .sl-markdown-content, .sl-markdown-content, main[data-pagefind-body]",
	},
	{
		Framework: FrameworkNextra,
		GenGates:  []string{"next.js"},
		DOMAny:    []string{".nextra-toc", ".nextra-sidebar", ".nextra-breadcrumb", ".nextra-navbar"},
		Selector:  `main[data-pagefind-body="true"], article`,
	},
	{
		Framework: FrameworkFumadocs,
		DOMAny:    []string{"#nd-page", "#nd-docs-layout"},
		Selector:  "article#nd-page .prose, article#nd-page, main[data-layout-main]",
	},
	{
		Framework:   FrameworkMintlify,
		GenContains: []string{"mintlify"},
		DOMAny:      []string{"#content-area"},
		AssetSubs:   []string{"mintcdn.com"},
		Selector:    "#content-area .mdx-content, .mdx-content, #content-area",
	},
	{
		Framework:   FrameworkFern,
		GenContains: []string{"buildwithfern.com"},
		DOMAny:      []string{"#fern-docs", "#fern-header", "#fern-sidebar", "main.fern-main"},
		Selector:    ".fern-prose, main.fern-main article",
	},
	{
		Framework:   FrameworkGitBook,
		GenContains: []string{"gitbook ("},
		DOMAny:      []string{"[data-gb-sections]", "[data-gb-table-of-contents]", "[data-gb-site-header]"},
		Selector:    "main div.contents, main",
		Exclude:     `nav[aria-label="Breadcrumb"]`,
	},
	{
		Framework:   FrameworkMkDocsMaterial,
		GenContains: []string{"mkdocs-material", "material for mkdocs", "zensical"},
		GenGates:    []string{"mkdocs"},
		DOMAny:      []string{"[data-md-component]", "[data-md-color-scheme]", ".md-content"},
		Selector:    "article.md-content__inner, .md-content article, .md-content",
	},
	{
		Framework: FrameworkMkDocsRTD,
		GenGates:  []string{"mkdocs"},
		DOMAny:    []string{"body.wy-body-for-nav"},
		AssetSubs: []string{"css/theme_extra.css"},
		AssetVeto: []string{"_static/"},
		Selector:  "div[role='main'].document, div.rst-content, .wy-nav-content",
	},
	{
		Framework:   FrameworkMkDocs,
		GenContains: []string{"mkdocs-"},
		DOMAny:      []string{"#mkdocs-search-query", "#mkdocs_search_modal", "div.col-md-9[role='main']"},
		Selector:    "div.col-md-9[role='main'], div[role='main']",
	},
	{
		Framework: FrameworkSphinxFuro,
		DOMAny:    []string{"article#furo-main-content"},
		AssetSubs: []string{"furo.css", "furo.js"},
		Selector:  "article#furo-main-content",
	},
	{
		Framework: FrameworkSphinxBook,
		DOMAny:    []string{".sbt-scroll-pixel-helper"},
		AssetSubs: []string{"sphinx-book-theme.js", "sphinx-book-theme.css"},
		Selector:  "article.bd-article, main.bd-main",
	},
	{
		Framework: FrameworkSphinxPyData,
		DOMAny:    []string{"article.bd-article"},
		AssetSubs: []string{"pydata-sphinx-theme.js", "pydata-sphinx-theme.css"},
		Selector:  "article.bd-article, main#main-content",
	},
	{
		Framework: FrameworkSphinxRTD,
		DOMAny:    []string{"body.wy-body-for-nav", ".rst-content"},
		AssetSubs: []string{"_static/"},
		Selector:  ".rst-content div[itemprop='articleBody'], .rst-content, div[role='main']",
	},
	{
		Framework:   FrameworkAntora,
		GenContains: []string{"antora"},
		DOMAny:      []string{"article.doc"},
		AssetSubs:   []string{"_/css/site.css", "_/js/site.js"},
		Selector:    "article.doc",
	},
	{
		Framework: FrameworkDocsy,
		GenGates:  []string{"hugo"},
		DOMAny:    []string{".td-content", ".td-sidebar"},
		Selector:  "div.td-content",
	},
	{
		Framework: FrameworkHugoBook,
		GenGates:  []string{"hugo"},
		DOMAny:    []string{"article.book-article", ".book-menu"},
		Selector:  "article.book-article",
	},
	{
		Framework: FrameworkGeekdoc,
		GenGates:  []string{"hugo"},
		DOMAny:    []string{"article.gdoc-markdown", ".gdoc-page"},
		Selector:  "article.gdoc-markdown",
	},
	{
		Framework: FrameworkJustTheDocs,
		GenGates:  []string{"jekyll"},
		DOMAny:    []string{"div#main-content.main-content", ".side-bar"},
		AssetSubs: []string{"just-the-docs"},
		Selector:  "#main-content main, #main-content",
	},
	{
		Framework: FrameworkMdBook,
		DOMAny:    []string{"#mdbook-content", "nav#mdbook-sidebar", "#mdbook-page-wrapper"},
		Selector:  "#mdbook-content main, main",
	},
	{
		Framework:   FrameworkRustdoc,
		GenContains: []string{"rustdoc"},
		DOMAny:      []string{"[data-rustdoc-version]", "section#main-content.content"},
		Selector:    "#main-content",
	},
	{
		Framework: FrameworkGodoc,
		DOMAny:    []string{".Documentation-content", `[data-test-id="UnitDetails-content"]`},
		Selector:  `.Documentation-content, [data-test-id="UnitDetails-content"], article.go-Main-article`,
		Exclude:   ".Documentation-toc",
	},
	{
		Framework:   FrameworkJavadoc,
		GenContains: []string{"javadoc/"},
		Selector:    "main[role='main'], main",
	},
	{
		Framework:   FrameworkDoxygen,
		GenContains: []string{"doxygen"},
		DOMAny:      []string{"div#nav-path.navpath", "div#titlearea"},
		Selector:    "div.contents",
	},
	{
		Framework: FrameworkTypeDoc,
		DOMAny:    []string{".tsd-generator", ".col-content .tsd-panel"},
		Selector:  ".col-content, .tsd-panel",
		Exclude:   ".tsd-breadcrumb, .tsd-index-panel",
	},
	{
		Framework: FrameworkWriterside,
		DOMAny:    []string{`article.article[data-template="article"]`},
		AssetSubs: []string{"/writerside/apidoc/"},
		Selector:  "article.article",
	},
	{
		Framework: FrameworkReadMe,
		DOMAny:    []string{`meta[name="readme-deploy"]`, `[data-testid="RDMD"]`, "article.rm-Article"},
		Selector:  `[data-testid="RDMD"], div.rm-Markdown.markdown-body, article.rm-Article`,
	},
	{
		Framework: FrameworkIntercom,
		DOMAny:    []string{"div.article.intercom-force-break"},
		Selector:  "div.article_body, div.article.intercom-force-break",
	},
	{
		Framework: FrameworkDocus,
		DOMAny:    []string{".docus-sub-header"},
		Selector:  "[data-content-id]",
	},
	{
		Framework: FrameworkSphinx,
		DOMAny:    []string{"div.body", ".sphinxsidebar", "div.document", "a.headerlink"},
		AssetSubs: []string{"_static/documentation_options", "_static/doctools", "searchindex.js"},
		Selector:  "div.body section[id], div.body, article.bd-article, div[role='main'], div.document",
	},
}
