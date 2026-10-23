# Architecture

Raulí is built in layers, and each layer knows only about the one below it. Here is the whole stack, with the parts that exist marked.

```text
agent
  |   a name and a JSON object per call
rauli serve            the command, on standard input and output (exists)
  |
tools.Session          Guard and Vault plug in here             (exists)
  |
chrome.Browser, Page   starts the browser, drives its tabs      (exists)
  |
cdp.Conn               JSON messages over one WebSocket         (exists)
  |
Chromium
```

Five more pieces sit beside the stack, and all of them exist. A rules engine implements the Guard. A keychain implements the Vault. The `rauli` command builds a session from a configuration file and serves the tools to an agent. A live view reads a session's events and shows its tab. Recipes, small Scheme programs, become tools of their own. I'll go through the layers from the bottom.

## The DevTools client

`internal/cdp` is a small client for the DevTools Protocol, which is JSON messages over a WebSocket. It knows nothing about pages or the DOM. It sends a command, hands back the reply, and delivers the events the browser sends. One connection serves the whole browser: a command for a tab carries that tab's session id, so a single WebSocket is enough however many tabs are open.

A subscriber that stops reading loses its oldest events once 1024 are waiting. Blocking the read loop instead would stall every command on the connection, and a slow log shouldn't be able to do that.

## The browser

`internal/chrome` starts a Chromium-based browser and finds its DevTools address, which the browser prints on startup. It runs headless unless asked for a window. The profile is a new directory that's deleted at the end, or a directory you name, which keeps cookies and logins from one run to the next. It can use a proxy, save downloads into a directory, and run every request through a filter.

A `Page` is one tab. The operations on it go through the browser's own input pipeline: a click is a real mouse press and release at the element's position, and typing is real key events or an insert, so a page sees what it would see from a person. `Snapshot` reads the accessibility tree and gives the elements you can act on a short reference. `Screenshot` captures the viewport, the whole page or one element, with or without a window.

Two things happen without being asked. JavaScript dialogs are answered at once, so a page can't hang the session: an alert is accepted and anything that wants a decision gets "no". The dialog is kept, and the next result reports it. A navigation that turns into a download comes back as an aborted navigation, which is handled as "nothing to wait for".

## The tools

`internal/tools` owns one browser session and offers each operation as a tool: a name, a description and a JSON Schema for the arguments. The [Tools](tools.md) page lists them all. A session runs one call at a time, because a tab is a single thing to act on, and every call has a time limit, 45 seconds unless the session was built with another.

Two interfaces are the only way a decision or a secret reaches a tool.

```go
type Guard interface {
	CheckURL(ctx context.Context, r chrome.Request) error
	CheckAction(ctx context.Context, a Action) error
}

type Vault interface {
	Handles(ctx context.Context) ([]Handle, error)
	Secret(ctx context.Context, handle, field, pageURL string) (string, error)
}
```

The Guard is asked in two places. The browser's request filter calls `CheckURL` for every request a page makes, which covers scripts, images and frames as well as typed addresses. The tools call `CheckAction` before an action runs, with the kind of action, the page's address, a description of the element such as `button "Pay now"`, and a detail such as the address being opened. A nil error allows. Any other error refuses, and its text goes back to the agent. A Guard may block while a person decides, and it has to give up when its context ends.

The Vault gives out one field of one credential at a time, for the page the field is on. `fill_credential` asks for the secret, types it into the page and forgets it. The secret doesn't appear in the tool's result.

Every call also produces an `Event`, with the tool, its arguments, the page it ended on and how long it took, and a session delivers them to anyone who subscribed. The text typed by `type` is recorded by length only. A subscriber that falls behind loses events and never slows the browser.

A person can take the browser with `Hold`, and give it back with `Release`. Calls made while it's held wait, and give up with a clear error if it isn't returned within their time limit.

## What sits beside it

The rules engine lives in `internal/rules` and implements the Guard; the [Rules](rules.md) page covers it. The keychain is `internal/vault` and implements the Vault, and the [Keychain](keychain.md) page covers that. The command in `cmd/rauli` builds a session from a configuration file, using `internal/config`, and serves its tools over standard input and output, so an agent can use it without linking any Go. The live view in `internal/watch` is an HTTP handler that reads a session's events and shows its tab, and it uses `Hold` and `Release` for the moments when a person takes over. [Watching](watching.md) covers it.

Recipes are Scheme files that Raulí runs with [Brote](https://fclaude.github.io/brote/). Every step a recipe takes is a call to one of the tools, so the rules and the keychain apply to it as they do to an agent. [Recipes](recipes.md) covers them.
