# Custom Go import path: `dash.cha.sh`

Cha Dash's Go module path is **`dash.cha.sh`** (decision 18), so users can run:

```bash
go install dash.cha.sh/cmd/chadash@latest
```

To resolve that path, the `go` tool requests `https://dash.cha.sh/<import path>?go-get=1` and reads two `<meta>` tags from the response. The domain therefore has to answer **every path** (`/`, `/cmd/chadash`, `/internal/...`) with those tags whenever `?go-get=1` is present. Ideally the response is **HTTP 200**.

**Local builds don't need any of this.** `go.mod` just says `module dash.cha.sh`. The tags only matter once people outside the repo `go install` it, and for pkg.go.dev.

## The tags

```html
<meta name="go-import" content="dash.cha.sh git https://github.com/ban-red/cha-dash">
<meta name="go-source" content="dash.cha.sh https://github.com/ban-red/cha-dash https://github.com/ban-red/cha-dash/tree/main{/dir} https://github.com/ban-red/cha-dash/blob/main{/dir}/{file}#L{line}">
```

## Option A: Cloudflare Worker (any path, 200, coexists with a future website)

```js
const TAGS = `<!doctype html><meta charset="utf-8">
<meta name="go-import" content="dash.cha.sh git https://github.com/ban-red/cha-dash">
<meta name="go-source" content="dash.cha.sh https://github.com/ban-red/cha-dash https://github.com/ban-red/cha-dash/tree/main{/dir} https://github.com/ban-red/cha-dash/blob/main{/dir}/{file}#L{line}">
<meta http-equiv="refresh" content="0; url=https://github.com/ban-red/cha-dash">`;

export default {
  async fetch(request) {
    const url = new URL(request.url);
    if (url.searchParams.get("go-get") === "1") {
      return new Response(TAGS, { headers: { "content-type": "text/html; charset=utf-8" } });
    }
    return fetch(request); // pass through to whatever serves the site (or redirect to GitHub until there is one)
  },
};
```

## Option B: Caddy (if `dash.cha.sh` is served from your own infrastructure)

```caddyfile
dash.cha.sh {
	@goget query go-get=1
	handle @goget {
		header Content-Type "text/html; charset=utf-8"
		respond `<!doctype html><meta name="go-import" content="dash.cha.sh git https://github.com/ban-red/cha-dash">` 200
	}
	handle {
		redir https://github.com/ban-red/cha-dash 302
	}
}
```

This only serves `go-import`, the one tag `go` needs. `go-source` uses `{…}` placeholders that Caddy would try to expand; if you want it, escape the braces as `\{` `\}`.

The same works in nginx with a `location /` block that checks `$arg_go-get`. With Traefik, run a tiny static container behind a router for `Host(\`dash.cha.sh\`)`.

## Verify

```bash
curl -s 'https://dash.cha.sh/cmd/chadash?go-get=1' | grep go-import
```

Once the first version tag exists:

```bash
GOPROXY=direct go list -m dash.cha.sh@latest
```
