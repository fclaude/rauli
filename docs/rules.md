# Rules

The rules decide two things. The first is which addresses the browser may reach, and the second is what the agent may do once it's there. They sit in the `[rules]` table of the [configuration file](configuration.md), and they apply to everything the browser does, whether a person typed the address, a script fetched an image, or a page opened a frame.

## What you get with no rules

With no configuration at all, Raulí allows the web and little else:

- Any website is reachable, and so is the local network. A page on `localhost` or on a private address opens like any other.
- Only `http`, `https`, `ws` and `wss` addresses are reached. A `file:` address is refused, and so is a `data:` or `blob:` address as a page of its own.
- Link-local addresses, the `169.254.0.0/16` and `fe80::/10` ranges, are never reachable, even if a rule lists them. That is where cloud machines keep their credentials, and no page an agent needs lives there.
- Every action is allowed except `eval` and `upload`, which ask first. A script run in the page can do anything the page can, and an upload sends a file from this machine to a website.

That is the posture of a fresh install, and it's meant for trying things out. A real setup lists the sites the agent may visit under `rules.network.allow`, so that everything else is refused. A page the agent reads can contain text that tries to give it orders, and a short list of sites limits where such an order can take it. For anyone who wants the local network closed as well, `rules.network.block_private` exists.

## Who is asked

When a rule asks for approval and no way of answering has been chosen, the live view is who is asked while it's running. Without it, the rules refuse, and the refusal says why and how to change it:

```text
not allowed: this needs a person's approval (an upload sends a file from this machine to a website) and no way to ask one is set up: choose rules.approval.mode in the rules, or change the rule
```

To choose a way, set `rules.approval.mode`:

| Mode | What happens |
| --- | --- |
| `deny` | Nobody is asked. Every action that would ask is refused. |
| `terminal` | The question is put on your terminal, `/dev/tty`, so it works even while standard input and output carry the protocol. |
| `command` | Raulí runs a program, gives it the request as JSON on standard input, and treats exit status 0 as a yes. Any other status is a no. The program is started without a shell, and a relative name is looked up from the folder you ran `rauli` in, so write the full path. |

An answer that doesn't come within `rules.approval.timeout`, two minutes by default, is a no.

## Addresses

An address in `allow`, `deny` or a rule looks like an address: `scheme://host:port/path`. The pieces are optional, and a `*` matches any run of characters.

- A scheme of `https` also covers `wss`, and `http` also covers `ws`, because a page and its websocket belong to the same site. With no scheme, a pattern covers all four.
- `*.example.com` matches any subdomain at any depth and doesn't match `example.com` itself. List both to allow both.
- Without a port, only the scheme's own port matches: 80 for `http`, 443 for `https`. A `*` matches any port.
- `localhost` stands for the machine itself. It matches `localhost`, `127.0.0.1` and `[::1]`, on any port when you give none, since development servers use whatever port they like.
- A path is matched from the root, and `*` in it crosses slashes. With no path, every path matches. The query and the fragment are never part of the match.
- The host is matched without regard to case.

| Pattern | Matches | Doesn't match |
| --- | --- | --- |
| `https://example.com` | `https://example.com/any/path?q=1` | `https://www.example.com`, `http://example.com` |
| `https://*.example.com` | `https://a.b.example.com/` | `https://example.com/` |
| `http://localhost` | `http://127.0.0.1:3000/` | `http://192.168.0.5:3000/` |
| `https://example.com/docs/*` | `https://example.com/docs/a/b` | `https://example.com/blog/` |
| `https://example.com:8443` | `https://example.com:8443/` | `https://example.com/` |

Every row of that table is checked by a test.

A request is decided in this order. A match in `deny` refuses it. A match in `allow` lets it through. Then `block_private`, when set, refuses a local address that wasn't in `allow`. Last, if `allow` isn't empty, anything left over is refused. This is why a development server is a single line in `allow` even with `block_private` on.

`block_private` looks at the address as written. It refuses `localhost`, names ending in `.local`, `.localhost` or `.internal`, and literal loopback, private and unspecified IP addresses. A public name that happens to resolve to a private address isn't caught, because the rules never see where a name goes. If that matters, a proxy is the place to enforce it.

## Actions

The kinds of action are `navigate`, `click`, `type`, `key`, `select`, `upload`, `eval` and `credential`. The tools that only look, such as `snapshot` and `screenshot`, don't ask the rules anything. The [security model](security.md#what-the-guard-sees) lists exactly which tools do.

A rule says what it matches and what to decide. Every key that's set has to match, and the first rule that matches decides:

| Key | What it matches |
| --- | --- |
| `kind` | One of the kinds above, or `*`. |
| `element` | A description of the target, such as `button "Pay now"`, matched without regard to case. |
| `url` | The page the action happens on. |
| `detail` | The extra fact for the kind: the address for `navigate`, the file's path for `upload`, the login and field for `credential`. Typed text is never in it. |
| `decision` | `allow`, `ask` or `deny`. |
| `reason` | A sentence shown to the person who is asked, and to the agent when the answer is no. |

`actions.default` is what happens when no rule matches. Left empty, everything is allowed except `eval` and `upload`. Set it to `allow` to stop those two from asking, or to `ask` or `deny` for the opposite stance.

## Three recipes

I ran each of these against small pages on my machine, and a test in the repository does it again on every change.

### Keep the agent on one site

```toml
[rules.network]
allow = ["http://127.0.0.1:8765"]
```

A page on that address opens. Anything else is refused, and the agent sees why:

```text
not allowed: http://127.0.0.1:8766/ is not on the list of sites the rules allow
```

The tab stays on the page it was on, so the agent can carry on from there. Images, scripts and frames from other sites are refused too, which usually breaks the page's analytics and nothing you wanted.

### Ask before anything that looks like paying

```toml
[[rules.actions.rule]]
kind = "click"
element = "*pay*"
decision = "ask"
reason = "payments need a person"

[rules.approval]
mode = "command"
command = ["/home/you/bin/ask-me"]
timeout = "20s"
```

On a checkout page with a "Pay now" button and a "Continue shopping" button, a click on the second goes through without a question. A click on the first runs `ask-me` with this on standard input:

```json
{"id":"","tool":"click","kind":"click","url":"http://127.0.0.1:8765/pay.html","element":"button \"Pay now\"","detail":"","reason":"payments need a person","time":"2026-10-22T19:33:45.078151041-07:00"}
```

If the program exits with 1, the agent is told:

```text
not allowed: a person did not approve this (payments need a person)
```

If it exits with 0, the click happens. A pattern as loose as `*pay*` also matches "Display" and "Repay", so it errs towards asking. That's the right direction for a payment.

### Refuse uploads

```toml
[[rules.actions.rule]]
kind = "upload"
decision = "deny"
reason = "nothing leaves this machine through a form"
```

```text
not allowed: refused by rule 1: nothing leaves this machine through a form
```

Without that rule, an upload asks. With no way of asking, it's refused anyway, so this recipe matters when you've set up approvals and still want uploads to be a flat no.

## The audit log

```toml
[rules.audit]
file = "audit.jsonl"
requests = "denied"
```

The log has one JSON object per line, of two kinds. A line with `"type":"decision"` records something the rules decided: every action, whether it was allowed, refused or approved, and the rule that decided, and by default each request they refused. A line with `"type":"tool"` records a tool call, with the page it ended on, how long it took and the error if there was one. With `requests = "all"` the log also records every request a page makes, which gets long quickly.

Addresses are logged without their query, fragment and login, since that's where tokens tend to be. Typed text is never recorded, only its length. A relative `file` is taken from the configuration file's folder.
