# 7. Identity

Who a member is, how they sign in, which forge accounts are theirs, what they may do, and what they can see.

## Ways in

Three ways to sign in, all resolving to the same member, plus one machine identity:

| Method | Suits | Forge account link |
|---|---|---|
| Local account | Self-hosting, zero dependencies | Verified separately: by authorizing once with the forge, or by an admin |
| Forge sign-in | The most natural fit | Linked and verified by signing in |
| OIDC | Single sign-on | Linked by also authorizing with the forge once, or from a claim the admin marks as trusted |
| API token | Executors, bridges, scripts | Belongs to a member; scoped and revocable |

## Data

| Record | Holds |
|---|---|
| `member` | Display name, state (active or disabled), permissions |
| `member_identity` | Member, provider (`local`, `oidc`, or a forge kind), subject, verified, linked time |
| `local_credential` | Member, username, password hash |
| `api_token` | Member, name, prefix, hash, scopes, created, last used, expiry, revoked |
| `browser_session` | Hash of the session ID, member, created, last seen, expiry |
| `invite` | Hash of the invite code, who created it, expiry, who used it |
| `repo_access` | Repository, forge account, access level — from repository refreshes |

An identity's **subject** is the provider's stable identifier: a forge's numeric account ID; an OIDC issuer together with its subject claim; a local account's own ID. Roles are derived from forge subjects ([Verdicts](04-verdicts.md#roles)).

## Permissions

Park's own permissions are distinct from the roles a member holds on pull requests.

| Permission | Allows |
|---|---|
| Member (everyone) | See the pull requests their forge accounts can read; act on groups; mark nudges; manage their own automation, identities, and tokens; retry or dismiss their own tasks |
| Rules | Draft and publish rule sets |
| Admin | Installations and repositories; members, invites, and permissions; sign-in and model providers; grouping settings; retry or dismiss anyone's tasks |

## Flow: first start

1. When Park starts with no members, it prints a one-time setup code to its log.
2. The setup page asks for that code, then creates the first administrator — with a local password, or through a sign-in provider configured in the file.
3. The code stops working once it is used.

There is never a default account or a default password.

## Flow: sign in

1. **Local**: a username and password, checked against the argon2id hash. Failures are rate-limited per account and per client address.
2. **Forge or OIDC**: the authorization-code flow with PKCE and a state parameter. The returned subject is looked up in `member_identity`.
3. **A known identity** signs its member in. **An unknown one** becomes a new member only if admission allows it — the person holds an invite, or belongs to a forge organization or OIDC group the admin allowed — and is refused otherwise, with "ask an admin for an invite".
4. A signed-in browser holds a session: a random ID in a cookie that is `HttpOnly`, `Secure`, and `SameSite=Lax`, stored only as a hash on the server. Requests that change state must also carry a CSRF header tied to the session.

## Flow: link a forge account

1. The member chooses "Connect a forge account" and authorizes once with the forge.
2. Park records the forge kind and account ID as a verified identity of the member.
3. An account already linked to another member is refused; an admin sorts it out.

An admin can also link an account by hand, and the link records who made it. Typing in a username is never enough: if it were, anyone could read anyone's queue.

## Flow: API tokens

1. A member creates a token with a name, its scopes, and an optional expiry.
2. Park shows the token once. It stores only a hash; the token's prefix — `park_` and a short ID — identifies it in lists and to secret scanners.
3. Each use updates its last-used time.
4. Revoking takes effect at once. A lease the token held ends and is verified like an expired one ([Tasks](06-tasks.md)).

| Scope | Allows |
|---|---|
| `read` | Read everything the member can see |
| `tasks:work` | Claim, heartbeat, report, and release the member's tasks |
| `assertions:write` | Post and retract grouping assertions; the token is the assertions' source |

## Visibility

A member sees a repository's pull requests only if one of their linked forge accounts can read that repository on the forge. Repository refreshes record which linked accounts have access. Members with no linked forge account see only their own settings. Every list, every read, and every event stream is filtered this way — including what bridges and executors can read with their tokens.

## Flow: disable a member

Disabling a member revokes their tokens and sessions, and ends their leases. Their forge accounts still take part in pull requests, so balls they hold stay with them, shown as held by someone outside Park; history keeps their name. Members are never deleted.

## Rules

- **I1. No default account.** The first administrator needs the setup code.
- **I2. Passwords use argon2id**, and failed sign-ins are rate-limited.
- **I3. Links are verified.** A forge account is linked only through the forge or by an admin, and to one member only.
- **I4. Stable subjects.** Identities are keyed by the provider's stable identifier, never by a name.
- **I5. One member, many identities.** Starting with a local account and adding single sign-on later never means recreating a person.
- **I6. Tokens are shown once, stored hashed, scoped, and revocable.** Every lease and assertion records its token.
- **I7. Visibility follows the forge.**
- **I8. Members are disabled, never deleted.**
- **I9. Secrets stay out of logs and events**: passwords, tokens, session IDs, and provider credentials.
