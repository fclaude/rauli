# Raulí

Raulí lets a program use a web browser the way a person does. It starts a Chromium-based browser, usually without a window, and drives it over the DevTools Protocol, so a program can read a page as text, click, type, scroll and take screenshots. The page sees real mouse and keyboard events, which means sites behave as they do in the browser you already use.

It's named after the raulí, a Chilean beech and a close relative of the coigüe. [Coigüe](https://github.com/fclaude/coigue) is a small terminal editor and [Brote](https://github.com/fclaude/brote) is a small Scheme interpreter, and the three live next to each other.

## Where it stands

Two layers work and are tested against a real browser. `internal/chrome` starts the browser and drives its tabs, over a small DevTools client in `internal/cdp`. `internal/tools` turns that into the tools an agent calls: `navigate`, `snapshot`, `click`, `type`, `screenshot` and the rest, listed in [docs/tools.md](docs/tools.md).

The tools put two interfaces in the way of everything that matters. A Guard is asked before the browser makes a request and before an action runs, and a Vault hands a credential to the page without the agent seeing it. The interfaces are in place. A rules engine, a keychain, the command that serves the tools to an agent and a live view for watching the browser are being added, and until they land `internal/rules` and `internal/vault` are empty packages that compile.

## Building and testing

You need Go 1.26 or newer and a Chromium-based browser. The tests start a real headless browser and drive it against pages they serve themselves, so they need no network; without a browser they skip.

```bash
go build ./...
go test -race ./...
```

[docs/getting-started.md](docs/getting-started.md) runs a short program against a real page and shows what comes back.

## Documentation

The pages in `docs/` are the documentation, and a Zola site in `site/` is built from them. The Tools page is generated from the tools' own schemas. `site/README.md` says how to build the site and how its accessibility is checked. The [security model](docs/security.md) lists exactly what the Guard and the Vault do and don't protect against, and it's worth reading before pointing Raulí at anything with a login.

## Licence

MIT. See `LICENSE`.
