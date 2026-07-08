# Authentication & Authorization

A `mongreldb-server` daemon runs in one of three modes:

1. **Open** (default) - no auth required.
2. **Bearer token** (`--auth-token <TOKEN>`) - every request must carry an
   `Authorization: Bearer <TOKEN>` header.
3. **HTTP Basic** (`--auth-users`) - every request must carry an
   `Authorization: Basic <base64(user:pass)>` header.

The Go client supports all three through `NewClient` options. This guide shows
each mode, how to inspect what was sent, and how to manage users and roles via
SQL when the server is in Basic mode.

---

## Bearer token mode

Start the daemon with a token:

```sh
mongreldb-server --auth-token s3cret-token
```

Connect with `WithToken`. The token is sent as `Authorization: Bearer ...` on
every request.

```go
db := mdb.NewClient("http://127.0.0.1:8453",
	mdb.WithToken("s3cret-token"),
)

ok, err := db.Health(ctx)
if err != nil {
	if errors.Is(err, mdb.ErrAuth) {
		log.Fatal("bad or missing token")
	}
	log.Fatal(err)
}
fmt.Println("healthy:", ok)
```

A missing or wrong token surfaces as an error wrapping `mdb.ErrAuth` (HTTP
401/403).

### Where the token comes from

Hard-coding secrets in source is bad practice. Read it from the environment:

```go
token := os.Getenv("MONGRELDB_TOKEN")
if token == "" {
	log.Fatal("MONGRELDB_TOKEN not set")
}
db := mdb.NewClient(mdb.DefaultBaseURL, mdb.WithToken(token))
```

## Basic auth mode

Start the daemon with a users file or inline users:

```sh
mongreldb-server --auth-users
```

Connect with `WithBasicAuth`:

```go
db := mdb.NewClient("http://127.0.0.1:8453",
	mdb.WithBasicAuth("admin", "s3cret"),
)
```

The client base64-encodes `username:password` and sets
`Authorization: Basic ...` on every request.

## Token takes precedence

If you supply both, `WithToken` wins and Basic credentials are ignored. This
lets you layer an override without branching:

```go
db := mdb.NewClient(url,
	mdb.WithBasicAuth("fallback", "user"),
	mdb.WithToken("overrides-everything"),
)
```

## Custom transport and timeouts

`WithHTTPClient` installs a custom `*http.Client` (e.g. with a custom
transport for mTLS, proxies, or connection pooling). `WithTimeout` sets the
per-request timeout.

```go
db := mdb.NewClient(url,
	mdb.WithToken(token),
	mdb.WithTimeout(60*time.Second),
)

// Or a fully custom client:
db := mdb.NewClient(url,
	mdb.WithToken(token),
	mdb.WithHTTPClient(&http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{ /* TLS, proxy, pooling, ... */ },
	}),
)
```

## Verifying what gets sent

The auth header is applied in `Client.applyAuth`, called from every request.
For debugging, point the client at a local echo server or watch the daemon
logs. A quick integration test pattern using `httptest`:

```go
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	fmt.Println(r.Header.Get("Authorization")) // inspect the header
	w.WriteHeader(200)
}))
defer srv.Close()

db := mdb.NewClient(srv.URL, mdb.WithToken("abc"))
_, _ = db.Health(context.Background())
// prints: Bearer abc
```

## User and role management via SQL

When the daemon is in Basic auth mode, users and roles live in the catalog
and are managed with SQL. Run these statements through `Client.SQL`.

### Create a user

```go
db.SQL(ctx, "CREATE USER alice WITH PASSWORD 'hunter2'")
```

### Alter a user

Change a password:

```go
db.SQL(ctx, "ALTER USER alice WITH PASSWORD 'new-password'")
```

Grant the admin role:

```go
db.SQL(ctx, "ALTER USER alice ADMIN")
```

`ALTER USER ... ADMIN` is how you promote a user to full administrative
privileges (table creation/drop, compaction, user management). Use it
sparingly.

### Drop a user

```go
db.SQL(ctx, "DROP USER alice")
```

### Roles and grants

```go
db.SQL(ctx, "CREATE ROLE analyst")
db.SQL(ctx, "GRANT SELECT ON orders TO analyst")
db.SQL(ctx, "GRANT analyst TO alice")
db.SQL(ctx, "REVOKE SELECT ON orders FROM analyst")
db.SQL(ctx, "DROP ROLE analyst")
```

Exact grant syntax mirrors the server's SQL flavor; consult the server's SQL
reference for the full `GRANT`/`REVOKE` grammar available in your build.

## Common pitfalls

**Auth errors look like other errors without `errors.Is`.** A 401/403 maps to
`mdb.ErrAuth`; a 404 maps to `mdb.ErrNotFound`. Always discriminate with
`errors.Is` rather than string-matching `err.Error()`.

**Forgetting to set auth in production.** A client built with `NewClient(url)`
and no options sends no credentials. Against an auth-enabled daemon, every
call fails with `ErrAuth`. Centralize client construction so the auth option
is never accidentally dropped.

**Sharing one client across goroutines is fine; sharing credentials across
users is not.** A `*Client` is safe for concurrent use, but it carries one
identity. If you serve multiple authenticated users, build a client per user
(or per request) with that user's token.

**Token in version control.** Put secrets in the environment, a secret
manager, or a file outside the repo. Never commit a real token.

## Next steps

- [errors.md](errors.md) - `ErrAuth` and the rest of the error hierarchy
- [quickstart.md](quickstart.md) - the full end-to-end walkthrough
