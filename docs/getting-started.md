# Getting started

Raulí is a set of Go packages with a command, `rauli`, on top of them. The command is new and this page doesn't cover it yet. What we'll do here is run the tests and drive the tools from a short Go program.

## What you need

You need Go 1.26 or newer and a Chromium-based browser. Raulí looks for the browser in this order: the path in `RAULI_CHROME`, then `google-chrome-stable`, `google-chrome`, `chromium`, `chromium-browser` and `chrome` on your `PATH`, then a Chromium that Playwright downloaded, then the usual macOS application folders. If it finds none it says so, and the tests that need a browser skip themselves.

Raulí never touches your own browser profile. Each run gets a new, empty profile directory that is deleted when the browser closes. Chromium's own sandbox stays on. It's switched off only when you run as root, or when you set `RAULI_NO_SANDBOX`, which is what a container usually needs.

## Run the tests

```bash
git clone https://github.com/fclaude/rauli
cd rauli
go test ./...
```

The tests start a real headless browser and drive it against small pages that the tests serve themselves, so they need no network. They take about a minute.

## Read a page

The example below opens an address, prints the page as text, follows the first link and takes a screenshot. It lives in the repository as `docs/examples/readpage`, and CI builds it, so it can't fall out of step with the code.

<!-- include: docs/examples/readpage/main.go go -->

We need something to read. Any folder with an `index.html` will do, and a second page for the link to lead to. These are the two files used here:

```html
<!doctype html><html lang="en"><head><title>Example Domain</title></head><body>
<h1>Example Domain</h1>
<p>This domain is for use in illustrative examples in documents.</p>
<p><a href="/more.html">More information...</a></p></body></html>
```

```html
<!doctype html><html lang="en"><head><title>More information</title></head><body><h1>More information</h1><p>You have followed the first link.</p></body></html>
```

Save them as `index.html` and `more.html` in an empty folder, serve it, and run the example from the repository in another terminal:

```bash
python3 -m http.server 8765 --bind 127.0.0.1
```

```bash
go run ./docs/examples/readpage http://127.0.0.1:8765/ out.png
```

This is what it printed when we ran it:

```text
Page: Example Domain
URL: http://127.0.0.1:8765/

- heading "Example Domain" [level=1]
- text: "This domain is for use in illustrative examples in documents."
- link "More information..." [ref=e1] -> http://127.0.0.1:8765/more.html

--- clicking e1
Page: More information
URL: http://127.0.0.1:8765/more.html

- heading "More information" [level=1]
- text: "You have followed the first link."

Screenshot of http://127.0.0.1:8765/more.html (1280x800)
```

`out.png` is a 1280 by 800 picture of the second page. No window opened at any point.

## Reading a snapshot

A snapshot is the page's accessibility tree, which the browser already works out for screen readers, cut down to the parts a reader needs. Headings, text, links and form controls are there. Wrapper elements that mean nothing are left out. A link's text belongs to the link, so it isn't printed twice.

Every element you can act on carries a reference such as `e1`, and the tools that act take it: `click` with `ref` set to `e1` is what the example did. A reference lasts until the page changes. After a navigation the old ones are gone, and asking for one is an error that tells you to take a new snapshot. We chose that over guessing, because a click that lands on whatever moved into the old spot is much worse than an error.
