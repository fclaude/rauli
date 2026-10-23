# Keychain

The keychain holds the logins an agent may use without ever seeing them. A login has a name, some fields such as `user` and `password`, and the origins where it may be used. The agent asks `fill_credential` for a field of a login by name. The tool takes the value from the keychain, types it into the page, and reports only that it did. The value isn't in the result, in the log, or in a snapshot of a password field.

The [security model](security.md#credentials) says what that protects and where it stops. This page is about setting it up and using it.

## Choosing where the logins live

The `[keychain]` table of the [configuration file](configuration.md) names a backend:

- `keyring` keeps logins in the system's secret store: the Secret Service on Linux, the Keychain on macOS.
- `file` keeps them in one encrypted file, opened with a passphrase.
- `command` keeps the secrets in a password manager, or in any program of yours that can print one value. Raulí holds only each login's name, fields and origins.
- `memory` keeps them until the program stops. It exists for tests.

With no backend there's no keychain, and `rauli keychain` says so:

```text
rauli keychain: no keychain is set up: add a [keychain] section to the configuration, with a backend of keyring, file or command
```

I tested the file and command backends for everything on this page. The examples use the file backend until the section on the command backend.

```toml
[keychain]
backend = "file"
path = "keychain"
passphrase_env = "RAULI_PASSPHRASE"
```

The passphrase comes from the environment variable named by `passphrase_env`, or from the output of `passphrase_command`, or, if neither is set, from a question on your terminal. A program that serves the tools to an agent usually has no terminal, so give it one of the first two. The file is created the first time you add a login and is readable by its owner only.

## Adding a login

```bash
export RAULI_PASSPHRASE=a-long-passphrase
rauli keychain --config config.toml add --origin http://127.0.0.1:8766 shop user password
```

The values are asked for one at a time, with the secret ones hidden. Without a terminal they're read from standard input, one per line, in the order of the fields, which is how the examples here were run.

```text
Added shop (user, password), usable on http://127.0.0.1:8766.
```

A login needs the origins where it may be used, and leaving them out is an error. This is the check that stops a page from collecting a password that was meant for another site:

```text
rauli keychain: a login needs the origins where it may be used, such as --origin https://example.com. A login usable on every site is a decision to make on purpose: pass --anywhere if that is what you mean
```

`--anywhere` is for the rare login that really works on every site. Adding a name that already exists is refused unless you pass `--replace`.

A field called `totp` holds the seed of a one-time code, in the base32 form that an authenticator app shows when you set one up, or an `otpauth://` address, which may carry the number of digits and the period. The code is what gets typed, and it changes every thirty seconds.

## Looking at what's there

```text
$ rauli keychain list
NAME  FIELDS          ORIGINS
shop  password, user  http://127.0.0.1:8766

$ rauli keychain show shop
name: shop
fields: password, user
origins: http://127.0.0.1:8766
```

Neither prints a value, and no command does. If you've lost a password, the place to look is wherever you got it from.

## Checking that a login works

```text
$ rauli keychain test shop http://127.0.0.1:8766/
ok: shop can fill password, user on http://127.0.0.1:8766/

$ rauli keychain test shop http://127.0.0.1:8765/
rauli keychain: the login "shop" may not be used on http://127.0.0.1:8765/: it is for http://127.0.0.1:8766
```

For a login with a `totp` field, `test` also checks that the seed produces a code. A wrong passphrase is reported as such:

```text
rauli keychain: the keychain file could not be opened: the passphrase is wrong, or the file has been changed
```

`rauli keychain remove shop` deletes a login.

## Keeping the secrets in your password manager

With the `command` backend the logins are described in the configuration, and their values stay wherever you keep them. Raulí runs your program for one field at the moment it is typed into a page.

```toml
[keychain]
backend = "command"
command = ["/usr/local/bin/get-secret"]
timeout = "10s"

[[keychain.handles]]
name = "shop"
fields = ["user", "password"]
origins = ["https://shop.example.com"]
```

For the login `shop` and the field `password`, the program is run as `get-secret shop password`, with `RAULI_HANDLE`, `RAULI_FIELD` and `RAULI_ORIGIN` set in its environment, no standard input and an empty folder to work in. It prints the value on standard output. One trailing newline is removed, and at most 64 KiB is read. The program only runs for a page that the login's origins allow, so a page outside them never reaches it. A login that really works everywhere says `anywhere = true`, on purpose, in place of `origins`.

A field called `totp` is the code itself here, so your program works it out. With the other backends it's a seed. `rauli keychain list`, `show` and `test` work as before, and I ran a sign-in through a small script this way and got the same page as with the file backend. `add` and `remove` don't, because the logins are in the configuration:

```text
rauli keychain: with the command backend the logins are the [[keychain.handles]] tables of the configuration, and their values are in your password manager, so rauli keychain cannot add or remove one: edit the configuration
```

## Using it from an agent

The agent calls `credentials` to list the logins, with their fields and origins and without values, and then `fill_credential` for each field, giving the element to fill from its latest snapshot. I did exactly that against a small sign-in page: `type` for the user name, `fill_credential` for the password, and a click on the button.

```text
## fill_credential {"ref": "e2", "handle": "shop", "field": "password"}
Filled textbox "Password" with the password of "shop".
```

The next run, with the same profile, found the site signed in:

```text
Page: Account
URL: http://127.0.0.1:8766/

- heading "Welcome back, ana" [level=1]
- text: "You are signed in."
```

The same call on a throwaway profile showed "You are not signed in." That is the difference a profile makes, and it's why the login flow below exists.

## Signing in by hand

Some sites won't let a program sign in: they ask for a code sent to a phone, or they show a check meant for a person. Raulí doesn't try to get past those. The way through is to sign in yourself, once, in a profile you keep:

```bash
rauli watch --profile ~/.local/share/rauli/profile https://example.com/login
```

Open the address it prints, choose Take control, and sign in as you would in any browser. Stop the command. A program that later runs with the same `--profile` finds the browser already signed in. The profile holds your cookies, so the agent must never read that folder, and no tool does. [Watching](watching.md) has the rest.
