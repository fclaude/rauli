# Raulí

Raulí lets a program use a web browser the way a person does. It starts a Chromium-based browser, usually without a window, and drives it over the DevTools Protocol, so a program can read a page as text, click, type, scroll and take screenshots. The page sees real mouse and keyboard events, which means sites behave as they do in the browser you already use.

It's named after the raulí, a Chilean beech and a close relative of the coigüe. [Coigüe](https://github.com/fclaude/coigue) is a small terminal editor and [Brote](https://github.com/fclaude/brote) is a small Scheme interpreter, and the three live next to each other.

## What it does

A program asks for a page and gets it back as text, with a reference on each element it can act on. It then clicks, types, scrolls, takes screenshots and so on, through [22 tools](docs/tools.md). The `rauli` command serves them to any program that speaks the Model Context Protocol, over standard input and output, and from a shell it can print a page or save a screenshot.

Two things stand between the tools and the web. A rules engine decides which addresses the browser may reach and which actions need a person's approval, and a keychain hands a login to the page without the program ever seeing it. A live view shows the browser while it works and lets you take the tab over.

Raulí runs on Linux and macOS. A fresh install allows any website, so a real setup begins with a list of the sites the agent may visit; [Rules](docs/rules.md) explains how.

## Install

There's no release yet, so install from the main branch, and ask the program to check that it can find a browser:

```bash
go install github.com/fclaude/rauli/cmd/rauli@main
rauli doctor
```

[docs/getting-started.md](docs/getting-started.md) goes on from there.

## Building and testing

You need Go 1.26 or newer and a Chromium-based browser. The tests start a real headless browser and drive it against pages they serve themselves, so they need no network; without a browser they skip.

```bash
go build ./...
go test -race ./...
```

## Documentation

The pages in `docs/` are the documentation, and a Zola site in `site/` is built from them and published at <https://fclaude.github.io/rauli/>. The [command](docs/command.md) and [configuration](docs/configuration.md) pages say what the program takes, and the Tools page is generated from the tools' own schemas. `site/README.md` says how to build the site and how its accessibility is checked. The [security model](docs/security.md) lists exactly what the Guard and the Vault do and don't protect against, and it's worth reading before pointing Raulí at anything with a login. To report a problem in private, see `SECURITY.md`.

## Licence

MIT. See `LICENSE`.
