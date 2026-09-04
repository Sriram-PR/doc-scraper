# Records the README demo GIF. Run from this directory with a doc-scraper
# binary built next to it:  go build -o doc-scraper ../cmd/doc-scraper && vhs demo.tape
Output demo.gif

Set FontSize 15
Set Framerate 20
Set Width 1040
Set Height 560
Set Padding 14
Set TypingSpeed 35ms

Hide
Type "rm -rf demo-output demo-state && clear"
Enter
Show

Type "./doc-scraper crawl -config config.yaml -site click_docs"
Sleep 800ms
Enter
Sleep 2500ms
Hide
Wait@120s
Type "clear"
Enter
Show

Type `./doc-scraper search -config config.yaml -limit 3 "how do I group commands"`
Sleep 800ms
Enter
Sleep 4s

Type `./doc-scraper search -config config.yaml -limit 3 "prompt the user for input"`
Sleep 800ms
Enter
Sleep 4s

Type "# same index, for agents: the search_docs MCP tool (Claude Code / Desktop)"
Sleep 4s
