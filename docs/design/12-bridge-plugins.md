# 12. Bridge plugins

A bridge Park runs reads one chat, on one platform, through a plugin: an executable that knows the platform and nothing of Park's. This chapter is a plugin's contract. What Park does with what a plugin reads is in [Grouping](03-grouping.md#bridges-park-runs).

## Responsibilities

A plugin signs an account in to its platform, lists the chats the account can read, and reads a chat's root messages. Park keeps everything else: the credentials and the account's tokens, sealed with the server's key; what each bridge remembers between rounds; when each bridge runs; and the switches. A plugin keeps nothing between calls: what it needs comes with each request.

## Finding plugins

Park finds its plugins when it starts, in the directory `bridges.plugin_dir` names. Each regular, executable file there, other than those whose names start with a dot, is one plugin, and Park asks it what it is. A file that fails to answer, speaks another version of this protocol, or claims a kind another file claims stops Park from starting, with the reason. Without the directory, Park has no plugins.

An admin switches each plugin on or off ([Grouping](03-grouping.md#bridges-park-runs)). A plugin whose executable has left the directory still shows, by its kind, while its switch lasts, and can only be switched off.

## Calling a plugin

Park runs a plugin as `<plugin> <operation>`, with one JSON request on its standard input, and reads one JSON object from its standard output.

- **The environment** is short and fixed: `PATH`, `HOME`, `TMPDIR`, `TZ`, `LANG`, `LC_ALL`, `SSL_CERT_FILE`, `SSL_CERT_DIR`, and the proxy variables, when Park has them. None of Park's own variables reaches a plugin. It runs in its own directory.
- **Time.** A plugin has 10 seconds to describe itself, and 2 minutes for any other operation; then it is stopped.
- **One at a time.** Operations that use a bridge's account run one at a time, so that tokens a plugin renews are never used twice.
- **Size.** Park reads at most 16 MiB of an answer.
- **Standard error** goes to Park's log, and only when an operation fails, at most 1 KiB of it.
- **Exit status.** An answer, an error's included, comes with status 0. A plugin that exits otherwise, without an answer, has failed.

## Answers

Every answer is one JSON object. Besides its result, it may carry:

- `tokens`: the account's tokens, a JSON object that only the plugin reads. Park keeps the tokens a plugin answers with at once, and sends them with the next operation that needs them. A plugin that renews tokens answers with the new ones, also alongside an error, since the old ones are spent.
- `error`: `{"code": …, "message": …}`, instead of a result. `signin_lapsed` means the account can no longer be used: Park marks it lapsed, and an admin connects it again. Park logs any other error, with its message, and the operation fails.

## Operations

| Operation | Request | Answer |
|---|---|---|
| `describe` | Nothing | `protocol`, 1; `kind`, a lower-case letter, then up to 39 lower-case letters, digits, and hyphens; `name`, 1 to 100 characters; `fields`, the credentials' fields, up to 20, each a `key` (a lower-case letter, then up to 39 lower-case letters, digits, and underscores), a `label`, and whether it is `secret` |
| `authorize` | `credentials`; `redirect_uri`, where the platform sends the browser back; `state`; `code_challenge` and `code_challenge_method`, `S256`, for platforms that take PKCE | `url`, the platform's sign-in page, an http or https address |
| `exchange` | `credentials`; `redirect_uri`; the `code` the platform sent the browser back with; `code_verifier` | `account`, the platform's `id` for the account that signed in and its `name`, each 1 to 200 characters; `tokens` |
| `chats` | `credentials`; `tokens` | `chats`, those the account can read, each the platform's `id` for it, never a link, and its `name` |
| `threads` | `credentials`; `tokens`; `chat`; `since`, a time in RFC 3339 | `read`, how many root messages the plugin read; `threads`, those since the time that link anything, each with an `anchor`, the platform's ID for the message; `created`; `text`, the message as words, a paragraph a line, with each link's address after its label, and without mentions; `plain`, the same with links' labels only; and `links`, the links' addresses, in order |

`credentials` are the plugin's fields as an admin entered them, by key. Park checks every answer against this table, and an answer that does not fit is the plugin's failure.

A platform that answers a member who cannot read a chat with no messages, rather than an error, is answered as such: `read` is 0. Park then retracts nothing ([Grouping](03-grouping.md#bridges-park-runs)).

## Rules

- **B1. A plugin knows no forge.** It answers links as they are; Park finds the pull requests among them, with each forge kind's pattern for a pull request's address.
- **B2. A chat's text stays in the round.** Park neither stores nor logs a thread's text: it keeps IDs, titles, and the groups a model answered. A plugin writes no credential, token, or message text to its standard error.
- **B3. What a plugin answers is never a link where an ID is asked for.** Chats and anchors are the platform's IDs; links to them are rendered when they are shown.
- **B4. A bridge keeps its plugin.** Its anchors are its platform's, so a bridge Park runs never changes plugin.

## The reference plugin

`cmd/park-bridge-file` reads its chats from a JSON file, its one credential, and is the plugin Park's own tests run. Signing in goes straight back to Park with the PKCE challenge as the code, so that exchanging the code checks the verifier. Its tokens count the operations that used them, and every operation renews them, as a platform that rotates tokens does. An `error` in the file makes every operation but `describe` answer with it, and with renewed tokens all the same.
