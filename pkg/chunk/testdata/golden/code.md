# Installing

Install the CLI with your package manager, or build it from source if you need a development version. Both paths end with the same binary on your PATH.

```bash
# not a heading, a shell comment
## also not a heading
go install example.com/tool@latest

tool --version
```

## From a tarball

~~~
# tilde fence, still code
curl -LO https://example.com/tool.tar.gz
~~~

The tarball contains a single static binary. Verify its checksum before you unpack it.

````markdown
```go
# inside a nested fence
```
## still inside the outer fence
````

## Indented blocks

    # indented four spaces, a code block
    ```
    an indented fence is literal text
    ```

   ### Three spaces is still a heading

	# a tab-indented line is code

Text after the indented examples. Use `setTimeout` and `clearTimeout` to schedule work, and `parseURL` to split addresses.

## Unclosed fence

```python
def readConfig(path):
    # comment that looks like a heading
    return open(path).read()

# the fence never closes, so this stays code
