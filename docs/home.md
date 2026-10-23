# Raulí

Raulí lets a program use a web browser the way a person does. It starts a Chromium-based browser, usually without a window, and drives it over the DevTools Protocol. A program can read a page as text, click, type, scroll and take screenshots, and the page sees real mouse and keyboard events, so sites behave as they do in the browser you already use.

It's named after the raulí, a Chilean beech and a close relative of the coigüe. [Coigüe](https://github.com/fclaude/coigue) is a small terminal editor and [Brote](https://github.com/fclaude/brote) is a small Scheme interpreter, and the three live next to each other on GitHub.

## Where it stands

Two layers exist, and both are tested against a real browser. The first starts the browser and drives its tabs. The second turns that into the tools on the [Tools](tools.md) page, which is what an agent calls: `navigate`, `snapshot`, `click`, `type`, `screenshot` and the rest.

That second layer puts two interfaces in the way of everything that matters. A Guard is asked before the browser makes a request and before an action runs, and a Vault hands a credential to the page without the agent ever seeing it. Both interfaces are in place. The pieces that will fill them in, a rules engine and a keychain, aren't built yet. Neither is the command that serves the tools to an agent, or the live view for watching the browser. Their pages say so, and nothing on this site describes behaviour that doesn't exist.

## Where to go next

[Getting started](getting-started.md) runs a short Go program against a real page and shows what comes back. The [security model](security.md) is worth reading before you point Raulí at anything with a login, because it lists exactly what the Guard and the Vault do and don't protect against. The [architecture](architecture.md) page shows how the layers fit, and where the missing pieces will go.
