# 7. Identity

Who a member is, how they sign in, which forge accounts are theirs, what they may do, and what they can see.

## Ways in

Three ways to sign in, all resolving to the same member, plus one machine identity:

| Method | Suits | Forge account link |
|---|---|---|
| Local account | Self-hosting, zero dependencies | Verified separately: by authorizing once with the forge, or by an admin |
| Forge sign-in | The most natural fit | Linked and verified by signing in |
| OIDC | Single sign-on | Linked by also authorizing with the forge once, or from a claim the admin marks as trusted — only one that carries the forge's numeric account ID |
| API token | Executors, bridges, scripts | Belongs to a member, or to a bridge the member registered; scoped and revocable |

## Data

| Record | Holds |
|---|---|
| `member` | Display name, state (active or disabled), permissions |
| `sign_in_provider` | A configured way to sign in: a stable ID — the `{provider}` in `/auth/{provider}` — its kind (OIDC, or a forge), and its settings, such as an OIDC issuer and client. Configuration, like a forge's base address |
| `member_identity` | Member, forge, subject — the forge account's numeric ID — the admin who linked it by hand, if one did, and linked time. A link made through the forge is verified by construction. OIDC identities, keyed by their sign-in provider, arrive with OIDC |
| `local_credential` | Member, username, password hash |
| `api_token` | Member or bridge, name, prefix, hash, scopes, created, last used, expiry, revoked |
| `browser_session` | Hash of the session ID, member, created, last seen, expiry |
| `invite` | Hash of the invite code, who created it, expiry, who used it |
| `setup_code` | Hash of the one-time setup code, and its expiry; there is at most one |
| `repo_access` | Repository, forge account, access level — from repository refreshes |

An identity's **subject** is stable within its provider: a forge account's numeric ID, within that forge ([Facts](02-facts.md#data)); the OIDC `sub` claim, within that sign-in provider; a local account's own ID. No URL is stored with an identity: the OIDC issuer lives only in the provider's settings, and each sign-in checks that the token's `iss` matches it. The same numeric account on two forges, or the same `sub` from two providers, are two different identities. Roles are derived from forge subjects ([Verdicts](04-verdicts.md#roles)).

## Permissions

Park's own permissions are distinct from the roles a member holds on pull requests.

| Permission | Allows |
|---|---|
| Member (everyone) | See the pull requests their forge accounts can read; act on groups; mark nudges; manage their own automation, identities, and tokens; retry or dismiss their own tasks |
| Rules | Draft and publish rule sets |
| Admin | Installations and repositories; members, invites, and permissions; sign-in and model providers; grouping settings; retry or dismiss anyone's tasks |

Admins change settings from a browser session: no token scope allows it. Park keeps at least one active admin, so the last one can be neither demoted nor disabled.

## Flow: first start

1. When Park starts with no members, it prints a one-time setup code to its log — or takes one from its environment, where logs are readable by more people than the operator.
2. The setup page asks for that code, then creates the first administrator, with a local password ([D48](10-design-log.md#d48-the-first-administrator-signs-up-with-a-local-password)); they connect a forge account afterwards.
3. The code stops working once it is used, and expires after an hour; restarting Park issues a new one.

There is never a default account or a default password.

## Flow: sign in

1. **Local**: a username and password, checked against the argon2id hash. Failures are rate-limited per account and per client address.
2. **Forge or OIDC**: the authorization-code flow with PKCE and a state parameter, the state tied to the browser that started the flow by a short-lived cookie, so that nobody can finish a sign-in or a link in someone else's browser. The returned subject, with its provider, is looked up in `member_identity`.
3. **A known identity** signs its member in. **An unknown one** becomes a new member only if admission allows it — the person holds an invite, or belongs to a forge organization or OIDC group the admin allowed — and is refused otherwise, with "ask an admin for an invite". An invited person may also join with a local account: a username, a password, and a display name.
4. A signed-in browser holds a session: a random ID in a cookie that is `HttpOnly`, `Secure`, and `SameSite=Lax`, stored only as a hash on the server, for 30 days. Requests that change state must also carry a CSRF header tied to the session, and send their bodies as JSON, which other sites cannot send without asking first.

## Flow: link a forge account

1. The member chooses "Connect a forge account" and authorizes once with the forge.
2. Park records the forge and the account ID as a verified identity of the member.
3. An account already linked to another member is refused; an admin sorts it out.

An admin can also link an account by hand, and the link records who made it. Typing in a username is never enough: if it were, anyone could read anyone's queue. For the same reason, an OIDC claim is trusted for linking only if it carries the forge's numeric account ID, never a login.

A member can unlink an account, unless it is their last way in: a local password, or an account on a forge with a sign-in provider. A member who joined through forge sign-in has no password, and forge sign-in finds members only by their linked accounts, so unlinking that account would leave the next sign-in finding no one ([D65](10-design-log.md#d65-a-member-keeps-a-way-in)).

## Flow: API tokens

1. A member creates a token with a name, its scopes, and an optional expiry — for themselves, or for a bridge they registered ([Grouping](03-grouping.md#data)).
2. Park shows the token once. It stores only a hash; the token's prefix — `park_` and a short ID — identifies it in lists and to secret scanners.
3. Each use updates its last-used time.
4. Revoking takes effect at once. A lease the token held ends and is verified like an expired one ([Tasks](06-tasks.md)).

| Scope | Allows |
|---|---|
| `read` | Read everything the member can see |
| `tasks:work` | Claim, heartbeat, report, and release the member's tasks |
| `assertions:write` | Post and retract grouping assertions and proposals, for a bridge token only; the bridge, not the token, is their source |

## Visibility

A member sees a repository's pull requests only if one of their linked forge accounts can read that repository on the forge. Repository refreshes record which linked accounts have access. Members with no linked forge account see only their own settings. Every list, every read, and every event stream is filtered this way — including what bridges and executors can read with their tokens.

Visibility is decided pull request by pull request, not unit by unit: a group can span a public and a private repository. A viewer sees a unit's pull requests they can read and nothing derived from the others — not their titles, check names, reasons, holders, or a group name taken from them; hidden pull requests are counted, and shown only as "a pull request you cannot see". Writes are checked the same way: grouping actions, assertions, and every other write may name only pull requests the caller can see, and a name that is not visible is refused exactly as one that does not exist.

## Flow: disable a member

Disabling a member revokes their tokens and sessions, and ends their leases. Their forge accounts still take part in pull requests, so balls they hold stay with them, shown as held by someone outside Park; history keeps their name. Members are never deleted.

## Rules

- **I1. No default account.** The first administrator needs the setup code.
- **I2. Passwords use argon2id**, and failed sign-ins are rate-limited.
- **I3. Links are verified.** A forge account is linked only through the forge or by an admin, and to one member only.
- **I4. Stable subjects.** Identities are keyed by their provider and the provider's stable identifier, never by a name, and never with a URL.
- **I5. One member, many identities.** Starting with a local account and adding single sign-on later never means recreating a person.
- **I6. Tokens are shown once, stored hashed, scoped, and revocable.** Every lease and assertion records its token.
- **I7. Visibility follows the forge**, pull request by pull request, for reads and writes alike.
- **I8. Members are disabled, never deleted.**
- **I9. Secrets stay out of logs and events**: passwords, tokens, session IDs, and provider credentials.
- **I10. A member keeps a way in.** Their last way to sign in cannot be unlinked.
