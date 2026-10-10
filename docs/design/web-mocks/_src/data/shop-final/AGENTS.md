# Shop

A small shop: a Go API in `api/` and a static page in `web/`, served by `cmd/shop`.

- Build: `make build`
- Test: `go test ./...` (the web page has no tests yet)
- Lint: `make lint`
- Money is an integer count of cents (`int64`), everywhere. Never a float.
- Pages are counted from 1. A page below 1 is page 1; a page past the end is the last page.
- `seed/items.json` is the catalogue's source of truth; do not add items by hand elsewhere.
- Never read or write `.env` or anything under `secrets/`.
