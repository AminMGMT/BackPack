# Access control

The panel had one password and one level of access: anyone who could open it
could change anything, and nothing was written down afterwards.

Three things changed.

## Scopes

Every request is authorised at one function — `guard` in
`internal/webui/server.go` — against one of three scopes:

| Scope   | May                                                         |
|---------|-------------------------------------------------------------|
| `read`  | look at status, metrics, logs and the fleet                 |
| `write` | also create and edit tunnels, restart services, upgrade     |
| `admin` | also hand out and revoke credentials                        |

Signing in with the panel password is `admin`. Handing out a credential is
separate from using one, so a `write` token cannot mint itself a better one.

The vocabulary is the Telegram bot's, deliberately. The bot has had
`ReadOnly` / `canWrite` for a while; two permission models in one product is
how a gap opens between them.

## API tokens

For callers that are not browsers. A Prometheus scraper has no cookie, and
`/metrics` is an endpoint built for scrapers.

```
curl -H "Authorization: Bearer <token>" https://panel:8443/metrics
```

Create one under **Settings → Security → API tokens**. The secret is shown
once, when it is created, and never again — only its SHA-256 is stored, so a
token cannot leak with a backup.

The panel had a read-only token before and it was removed, correctly: nothing
issued it, so nothing rotated it. The reasons it was removed are addressed
rather than repeated:

- a **name** is required, so nobody is afraid to revoke an anonymous one;
- an **expiry** is required, so it cannot outlive what it was issued for;
- **last used** is recorded, so a dead token is recognisable as dead;
- they are **listed** on a screen an operator actually opens.

Prometheus:

```yaml
scrape_configs:
  - job_name: backpack
    authorization:
      credentials: <token>
    static_configs:
      - targets: ['panel.example.ir:8443']
```

## The record

Every action taken through the panel or with a token is recorded: who, from
where, what, and what the panel answered. Read it under **Settings → Security →
What has been done here**.

It is written by the authorisation guard, not by the handlers. A log each
handler writes for itself has one hole per handler somebody forgot to update,
and those holes are invisible until the day somebody goes looking. Written at
the choke point, the only way to act without being recorded is to act without
being authorised.

Reads are not recorded. The panel polls itself every few seconds, and thousands
of those lines would bury the handful that matter.

Refused attempts *are* recorded, and are often the more interesting line.

The record lives at `/etc/backpack/audit.json`, holds the last 5,000 entries,
and is readable only by root. An audit file that cannot be written never blocks
an action — a full disk must not lock an operator out of the tool they need to
fix it.
