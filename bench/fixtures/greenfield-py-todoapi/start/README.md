# todo: a tiny persistent TODO service

A TODO list stored in an SQLite file, as a Python package `todo` (the storage layer) plus a JSON-over-HTTP server on top
of it. Python 3.11, standard library only (`sqlite3`, `http.server`, `json`, `urllib`, `threading`, ...): no third-party
packages. This document is the complete specification: where it speaks it is exact, where it is silent the behaviour is
not specified and not tested.

## What to build

The repository root holds this file and `tests/`. Create the package `todo/` in the repository root (the tests run from
the root with `python3 -m unittest discover -s tests`, so `import todo` must work from there). These names must exist:

| import | what |
|--------|------|
| `from todo import TodoStore` | the storage class |
| `from todo import TodoError, NotFound, ValidationError` | the exceptions |
| `from todo.server import make_server` | builds the HTTP server |

How you split the code into modules (`todo/store.py`, `todo/server.py`, ...) is up to you.

## The todo object

A todo is a plain `dict` with exactly these four keys:

| key | type | meaning |
|-----|------|---------|
| `"id"` | `int` | the todo's id |
| `"title"` | `str` | the title, whitespace stripped at both ends |
| `"done"` | `bool` | `True` once completed (a real `bool`, not 0 or 1) |
| `"tags"` | `list[str]` | the todo's tags, normalised (see below), each once, sorted ascending |

Example: `{"id": 1, "title": "Buy milk", "done": False, "tags": ["home", "shop"]}`.

**Ids** are assigned 1, 2, 3, ... in the order todos are created. An id is never reused: after the todo with the highest
id is deleted, the next todo still gets a new, higher id. The numbering continues after the database is closed and
opened again.

**Titles.** A title must be a `str` with at least one non-whitespace character; it is stored stripped (`str.strip()`).
Anything else (`""`, `"   "`, `None`, `5`, ...) is invalid. Inner whitespace is kept as it is. Any other text, including
non-ASCII, is fine.

**Tags.** A tag is normalised by `tag.strip().lower()`. A normalised tag must be 1 to 20 characters long and consist only
of the characters `a-z`, `0-9`, `_` and `-` (so `" Work "` becomes `"work"`, and `"a b"`, `""`, `"x!"`, `"café"` and a
21-character tag are invalid). A value that is not a `str` is an invalid tag. The same rule applies to every tag
argument everywhere (`add`, `add_tag`, `remove_tag`, the `tag` filter of `list`, and in the HTTP API).

## Exceptions

```python
class TodoError(Exception): ...
class NotFound(TodoError): ...        # str(exc) == "not found"
class ValidationError(TodoError): ... # str(exc) is the message, see below
```

`ValidationError` messages are exactly `"title is required"` (invalid title) and `"invalid tag"` (invalid tag).
`NotFound` is raised when a todo id does not exist; any `int` that no todo has is unknown, however large.

## `TodoStore`

`TodoStore(path)` opens the SQLite database file at `path` (a `str`), creating it and its tables if needed, and keeps
the data it finds there. Use the `sqlite3` module from the standard library. `store.close()` closes the database.

| call | result |
|------|--------|
| `add(title, tags=())` | creates a todo and returns it. `tags` is a list, tuple or set of tags; duplicates (after normalising) collapse into one. The title is checked first, then the tags, in the order given; the first invalid one raises (`"title is required"` before `"invalid tag"`). A bare `str` as `tags` is invalid (`"invalid tag"`). **A failed `add` leaves no trace**: nothing is stored and no id is used up |
| `get(todo_id)` | the todo; `NotFound` if there is none |
| `list(tag=None, done=None)` | a list of todos, filtered and ordered as described below |
| `complete(todo_id)` | marks the todo done and returns it; completing a todo that is already done changes nothing and returns it; `NotFound` |
| `delete(todo_id)` | removes the todo and its tags; returns `None`; `NotFound` |
| `add_tag(todo_id, tag)` | adds a tag and returns the todo; adding a tag the todo already has changes nothing |
| `remove_tag(todo_id, tag)` | removes a tag and returns the todo; removing a tag the todo does not have changes nothing |

For `add_tag` and `remove_tag` the todo is looked up first: an unknown id is `NotFound` even if the tag is also invalid.

**`list(tag=None, done=None)`.** `tag`, when given, is normalised and checked like any tag (invalid: `ValidationError`)
and keeps only the todos that have it. `done`, when `True` or `False`, keeps only the done or only the open todos; `None`
keeps both. Both filters together keep the todos that satisfy both. The result is ordered like this: **open todos first,
by ascending id; then done todos, in the order in which they were completed** (the todo completed first comes first). A
todo that is completed again keeps its place. Example: create A (id 1), B (2), C (3), complete C, then A: the order is
B, C, A.

**Persistence.** Everything is stored in the file: a second `TodoStore` opened on the same path after the first one was
closed sees every todo, tag, completion order and the id counter.

**Threads.** A `TodoStore` must work when it is created in one thread and used in another, and when several threads use it
at the same time (the HTTP server creates the store in the thread that calls `make_server` and uses it in the thread
that runs `serve_forever`; with a threading server several requests run at once). `sqlite3` connections refuse to be
used across threads by default; deal with that.

## The HTTP server

`make_server(db_path, host="127.0.0.1", port=0)` returns an `http.server.HTTPServer` (or a subclass) that is bound and
listening but not serving. It owns a `TodoStore(db_path)`. The caller runs `server.serve_forever()` (usually in a
thread), finds the port in `server.server_address[1]`, and ends with `server.shutdown()` and `server.server_close()`;
`server_close()` must also close the database, so that a new server or `TodoStore` on the same file sees all the data.

All request and response bodies are UTF-8 JSON. Every response that has a body has the header
`Content-Type: application/json` (a `charset` parameter is allowed); the request's `Content-Type` is ignored.

### Routes

The path is matched exactly, without its query string. `{id}` is one or more ASCII digits; `{tag}` is one or more
characters other than `/`, percent-decoded before it is used.

| method and path | body | success | what it does |
|-----------------|------|---------|--------------|
| `POST /todos` | `{"title": str, "tags": [str, ...]}`; `tags` is optional | `201` and the todo | `TodoStore.add` |
| `GET /todos` | none; query `tag`, `done` | `200` and a JSON array of todos | `TodoStore.list` |
| `GET /todos/{id}` | none | `200` and the todo | `TodoStore.get` |
| `DELETE /todos/{id}` | none | `204`, no body at all | `TodoStore.delete` |
| `POST /todos/{id}/complete` | ignored | `200` and the todo | `TodoStore.complete` |
| `POST /todos/{id}/tags` | `{"tag": str}` | `200` and the todo | `TodoStore.add_tag` |
| `DELETE /todos/{id}/tags/{tag}` | none | `200` and the todo | `TodoStore.remove_tag` |

A todo in a response is the JSON object of the todo described above (`{"id": 1, "title": "Buy milk", "done": false,
"tags": ["home"]}`). The key order does not matter.

`GET /todos` takes the optional query parameters `tag` and `done`. `done` must be exactly `true` or `false` (lower
case). A parameter that is present but empty (`?tag=`, `?done=`) counts as present (and is therefore invalid). Other
parameters are ignored. `tag` is used as in `TodoStore.list`.

### Errors

Every error response has the JSON body `{"error": MESSAGE}` and one of these statuses:

| status | MESSAGE | when |
|--------|---------|------|
| `400` | `invalid json` | the body of `POST /todos` or `POST /todos/{id}/tags` is not valid JSON (an empty body included) or not a JSON object |
| `400` | `title is required` | the `title` of `POST /todos` is missing or invalid |
| `400` | `invalid tag` | a tag is invalid: in `tags` (which must be a list of strings, if present), in `tag` of `POST /todos/{id}/tags` (required), in the path of `DELETE .../tags/{tag}`, or in the `tag` query parameter |
| `400` | `invalid query` | the `done` query parameter is neither `true` nor `false` |
| `404` | `not found` | the path matches no route (`/`, `/todos/`, `/todos/abc`, `/todos/1/unknown`, ...), or no todo has that id |
| `405` | `method not allowed` | the path matches a route, but not for this method (`PUT /todos`, `DELETE /todos`, `GET /todos/1/complete`, `POST /todos/1`, `PATCH /todos/1`, ...) |

If several things are wrong, the first in this order is reported: **the route (404), the method (405), the todo (404),
then the body or query (400)**, and within those: `invalid json`, then `title is required`, then `invalid tag` for a
create; `invalid query` (the `done` parameter) before `invalid tag` (the `tag` parameter) for a list. So an unknown todo
id with an invalid body is a `404`, and `POST /todos` with `{"title": " ", "tags": ["!"]}` is `title is required`.

Unknown fields in a body are ignored.

## Examples

```python
store = TodoStore("todo.db")
store.add("  Buy milk ", tags=["Home", " shop", "home"])
# {'id': 1, 'title': 'Buy milk', 'done': False, 'tags': ['home', 'shop']}
store.add("Call Ann")                      # id 2
store.add("Write report", tags=["work"])   # id 3
store.complete(3); store.complete(1)
[t["id"] for t in store.list()]            # [2, 3, 1]: open first, then in completion order
[t["id"] for t in store.list(done=True)]   # [3, 1]
[t["id"] for t in store.list(tag="HOME")]  # [1]
store.delete(3)
store.add("Next")["id"]                    # 4, not 3
store.add("x", tags=["no good"])           # ValidationError("invalid tag"), nothing stored, no id used
```

```
POST /todos            {"title": "Buy milk", "tags": ["Home"]}
  -> 201 {"id": 1, "title": "Buy milk", "done": false, "tags": ["home"]}
POST /todos/1/complete
  -> 200 {"id": 1, "title": "Buy milk", "done": true, "tags": ["home"]}
GET /todos?done=false        -> 200 []
GET /todos?done=maybe        -> 400 {"error": "invalid query"}
GET /todos?tag=Bad%20Tag     -> 400 {"error": "invalid tag"}
POST /todos/7/tags  (body: not json)  -> 404 {"error": "not found"}     # the todo is checked first
POST /todos/1/tags  (body: not json)  -> 400 {"error": "invalid json"}
POST /todos/1/tags  {"tag": "Shop"}   -> 200 {"id": 1, ..., "tags": ["home", "shop"]}
DELETE /todos/1/tags/SHOP             -> 200 {"id": 1, ..., "tags": ["home"]}
DELETE /todos/1                       -> 204 (empty body)
GET /todos/1                          -> 404 {"error": "not found"}
PUT /todos                            -> 405 {"error": "method not allowed"}
```

## Checking your work

```
python3 -m unittest discover -s tests
```

`tests/test_example.py` shows how the tests start the server: `make_server(path)` (port 0), `serve_forever(poll_interval=0.01)`
in a thread, `urllib.request` requests without a proxy, `shutdown()` and `server_close()`. The complete acceptance tests,
which cover every row and rule above (the store used directly, including from several threads and across reopening
the database, and the server over HTTP), are run when your work is verified.
