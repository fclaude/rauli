# Recipes

Some jobs in a browser are the same every time. You open a page, find the form, fill in two fields and press a button. An agent can do that with the ordinary tools, but it spends a dozen calls on it and may get one of them wrong. A recipe is that job written down once, as a short Scheme program, and offered to the agent as a single tool.

Raulí runs recipes with [Brote](https://fclaude.github.io/brote/), the Scheme interpreter it embeds. A recipe has no more reach than an agent has. Every step it takes is a call to one of the browser tools, so the rules, the approvals, the keychain and the audit log apply to each of them as if the agent had made the call itself.

## A first recipe

This one lives in the repository as `docs/examples/recipes/headlines.scm`. The tests load and run it, so it can't fall out of step with the code.

<!-- include: docs/examples/recipes/headlines.scm scheme -->

`define-recipe` takes a name, a description that the agent reads, a list of parameters and a procedure. Each parameter is a name, a type (`"string"`, `"number"` or `"boolean"`) and a line saying what it is for. A fourth item is a default, and a parameter that has one may be left out. The procedure gets one argument per parameter, in the order they are listed. Whatever it returns is the result of the tool.

`navigate` and `snapshot` are the same operations the agent has. The snapshot comes back as the text you know from the [tools](tools.md) page, so a recipe reads a page the way an agent does, with string procedures. Everything else in that file is ordinary Scheme.

## Listing and running

`rauli recipes` loads files and says what they define. It starts no browser, so it works anywhere.

```text
$ rauli recipes docs/examples/recipes
headlines
    Read the headlines of a page: its second-level headings, one per line.
    url (string): The page to read [required]
    limit (number): How many headlines at most [default 10]
signup
    Fill in the sign-up form at a page and submit it. Returns what the next page says.
    url (string): The address of the form [required]
    name (string): The name to enter [required]
    email (string): The email address to enter [required]
```

`rauli run` starts a browser and runs one recipe in it. Parameters come as `--arg name=value`, and each value is read as the type the recipe declared. We served a small page with three headlines on a local address and ran it:

```text
$ rauli run docs/examples/recipes/headlines.scm --arg url=http://127.0.0.1:8791/newsroom --arg limit=2
Trees grow quietly
The shoot and the beech
```

With the default limit, all three come back:

```text
$ rauli run docs/examples/recipes/headlines.scm --arg url=http://127.0.0.1:8791/newsroom
Trees grow quietly
The shoot and the beech
A browser for everyone
```

A file with one recipe needs no `--recipe`. When it holds several, name the one you want with `--recipe NAME`. A script may also define a procedure called `main` that takes no arguments, and `rauli run` calls it when you name no recipe. `--save-images DIR` keeps the screenshots a run takes.

A value of the wrong type is a usage error, and nothing starts:

```text
$ rauli run docs/examples/recipes/headlines.scm --arg url=http://127.0.0.1:8791/newsroom --arg limit=many
rauli run: limit takes a number; got "many"
```

## A form

The second example fills in a sign-up form. `find-ref` looks through a snapshot for the first line that contains some text and returns the reference on that line, or `#f` when there is none. `type-text` and `click` take references, as the tools do.

<!-- include: docs/examples/recipes/signup.scm scheme -->

The page we ran it against has a Name field, an Email field and a button, and its answer page greets the person by name:

```text
$ rauli run docs/examples/recipes/signup.scm --arg url=http://127.0.0.1:8791/signup --arg name=Raulí --arg email=r@example.org
Thanks Raulí

We will write to r@example.org.
```

A recipe says plainly what went wrong, and names itself. Pointed at a page without the form, this one stops with its own message and exit status 1:

```text
$ rauli run docs/examples/recipes/signup.scm --arg url=http://127.0.0.1:8791/ --arg name=a --arg email=b
rauli run: recipe signup: the page does not have a Name field, an Email field and a Sign up button
```

## Offering recipes to an agent

`rauli serve --recipe FILE` adds the recipes in a file, or in a directory of `.scm` files, to the tools the server lists. The flag may be repeated. The same paths can live in the configuration file:

```toml
[recipes]
paths = ["recipes", "/opt/shared/recipes"]
deadline = "2m"
max_output = 100000
```

Relative paths are taken from the directory of the configuration file. A path that doesn't exist stops the server before it starts, and the message names the path. Each recipe appears as a tool called `recipe_` followed by its name, with a JSON Schema built from its parameters, so an agent sees `recipe_signup` beside `navigate` and `click`.

The checks on arguments are the ones the other tools have. A missing required parameter, an unknown one or one of the wrong type is an error that says which, and the recipe doesn't run.

## What a recipe can touch

The interpreter is frozen after the files load, and it is given no way to read a file, run a program or open a connection. `load` is the only procedure Brote has that reaches the machine, and recipes are refused it. A recipe that tries gets this:

```text
recipe peek: load: a recipe cannot use the machine's files, programs or network; it can only drive the browser through the browser tools
```

The browser is the only thing a recipe can reach, and every step goes through the same door as an agent's. Here are the rules used for the run below. They let the browser go to one address and refuse typing into the Email field:

```toml
[rules.network]
allow = ["http://127.0.0.1:8791/*"]

[[rules.actions.rule]]
kind = "type"
element = "textbox \"Email\""
decision = "deny"
reason = "recipes may not enter an email address"

[rules.audit]
file = "audit.jsonl"
```

```text
$ rauli run docs/examples/recipes/signup.scm --config config.toml --arg url=http://127.0.0.1:8791/signup --arg name=Raulí --arg email=r@example.org
rauli run: recipe signup: browser: type: not allowed: refused by rule 1: recipes may not enter an email address
```

The Name field was filled and the Email field was refused. The audit log recorded the `navigate`, the `snapshot`, both `type` calls and the decision on each, so a person reading it sees what the recipe did one step at a time, with the typed text replaced by its length. Sending the same recipe to a site the rules don't allow stops at the first `navigate`:

```text
rauli run: recipe headlines: browser: navigate: not allowed: https://example.com/ is not on the list of sites the rules allow
```

Passwords stay in the keychain. A recipe can call `fill_credential` through `browser`, which types the secret into the page without the recipe ever holding it.

## Limits

A recipe runs for two minutes at most. The limit is `deadline` in the configuration file, and it counts the steps in the browser too. When it is reached, the run stops and says how far it got, and the interpreter is ready for the next call:

```text
rauli run: recipe slow stopped after 2s, its time limit (0 steps in the browser so far)
```

What a recipe prints and returns is cut at `max_output` characters, with a line saying so at the end. A recipe may call another with `(recipe "name" '(("param" . value)))`, down to eight levels. Recipes are defined while the files load, and `define-recipe` is refused afterwards. Two recipes with the same name are refused, and the message names the file that defined the first.

## The procedures

These come with every recipe, besides the standard Brote procedures.

| Procedure | What it does |
| --- | --- |
| `(define-recipe name description params proc)` | Defines a recipe. Only while the files load. |
| `(navigate url)` | Opens an address. |
| `(snapshot)` | The page as text, with a reference on each element you can act on. |
| `(text)` | The page's visible text. |
| `(click ref)` | Clicks an element. |
| `(type-text ref string)` | Types into an element. |
| `(type-text-and-submit ref string)` | Types, then presses Enter. |
| `(press key)` | Presses a key. |
| `(screenshot)` | Takes a picture, which is returned with the result. |
| `(wait-for-text string)` | Waits until the text appears. |
| `(pause seconds)` | Waits. |
| `(find-ref snapshot text)` | The reference on the first snapshot line that contains the text, or `#f`. |
| `(page-url)`, `(page-title)` | The address and title of the current page. |
| `(browser tool alist)` | Calls any browser tool by name with `("name" . value)` pairs. The procedures above are written with it. |
| `(recipe name alist)` | Runs another recipe and returns its text. |

The helpers ask for no new snapshot after each step, which keeps recipes quick. Call `(snapshot)` when you need one. `browser` is how a recipe reaches a tool that has no helper, and each tool's arguments are on the [tools](tools.md) page.
