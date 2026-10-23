# Tools

A tool is an operation with a name and a set of arguments, described by a JSON Schema. A caller gives the name and a JSON object, and gets text back, plus a picture when the tool takes screenshots. The arguments are checked strictly. A name a tool doesn't have is an error, which keeps a misspelt argument from being quietly ignored while the caller believes it asked for something.

Most tools that change the page return a fresh snapshot, so the caller sees the result without asking. They take a `snapshot` argument, which defaults to true, for the cases where the caller would rather not spend the space. Only `http` and `https` addresses open, and `about:blank`. Addresses without a scheme are taken as `https`.

Every tool that acts goes through the Guard first, and the tools that only look skip it. The [security model](security.md) says which is which.

## Reference

This section is generated from the tools' own schemas when the site is built, so it can't say anything the code doesn't.

<!-- tools-reference -->
