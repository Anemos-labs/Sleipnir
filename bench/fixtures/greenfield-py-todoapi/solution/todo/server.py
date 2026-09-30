"""The JSON-over-HTTP layer."""

import json
import re
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qsl, unquote, urlsplit

from .store import NotFound, TodoStore, ValidationError

# (path pattern, {method: handler name}); the first pattern that matches the path decides
_ROUTES = [
    (re.compile(r"/todos"), {"GET": "list_todos", "POST": "create"}),
    (re.compile(r"/todos/([0-9]+)"), {"GET": "get", "DELETE": "delete"}),
    (re.compile(r"/todos/([0-9]+)/complete"), {"POST": "complete"}),
    (re.compile(r"/todos/([0-9]+)/tags"), {"POST": "add_tag"}),
    (re.compile(r"/todos/([0-9]+)/tags/([^/]+)"), {"DELETE": "remove_tag"}),
]


def _id(digits):
    """The id in a path; a number too long for any id is simply an id that no todo has."""
    return int(digits) if len(digits) <= 20 else 2**64


class Handler(BaseHTTPRequestHandler):
    server_version = "todo/1"

    def log_message(self, format, *args):  # keep the test output quiet
        pass

    # -- plumbing ----------------------------------------------------------------------------------------------

    def _reply(self, status, payload=None):
        self.send_response(status)
        if payload is None:  # 204: no body at all
            self.end_headers()
            return
        body = json.dumps(payload).encode("utf-8")
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _read_body(self):
        try:
            length = int(self.headers.get("Content-Length") or 0)
        except ValueError:
            length = 0
        return self.rfile.read(length) if length > 0 else b""

    def _json_object(self, raw):
        try:
            value = json.loads(raw.decode("utf-8"))
        except (UnicodeDecodeError, ValueError):
            raise ValidationError("invalid json") from None
        if not isinstance(value, dict):
            raise ValidationError("invalid json")
        return value

    def _dispatch(self):
        raw = self._read_body()  # always consumed, whatever the route
        parts = urlsplit(self.path)
        for pattern, methods in _ROUTES:
            match = pattern.fullmatch(parts.path)
            if match:
                break
        else:
            return self._reply(404, {"error": "not found"})
        name = methods.get(self.command)
        if name is None:
            return self._reply(405, {"error": "method not allowed"})
        try:
            status, payload = getattr(self, "_" + name)(self.server.store, raw, parts.query, *match.groups())
        except NotFound:
            return self._reply(404, {"error": "not found"})
        except ValidationError as err:
            return self._reply(400, {"error": str(err)})
        self._reply(status, payload)

    do_GET = do_POST = do_DELETE = do_PUT = do_PATCH = do_HEAD = do_OPTIONS = _dispatch

    # -- routes: (store, body, query, *path groups) -> (status, payload) ---------------------------------------------

    def _create(self, store, raw, query):
        body = self._json_object(raw)
        return 201, store.add(body.get("title"), body.get("tags", ()))

    def _list_todos(self, store, raw, query):
        params = {}
        for key, value in parse_qsl(query, keep_blank_values=True):
            params.setdefault(key, value)
        done = None
        if "done" in params:
            if params["done"] not in ("true", "false"):
                raise ValidationError("invalid query")
            done = params["done"] == "true"
        return 200, store.list(tag=params.get("tag"), done=done)

    def _get(self, store, raw, query, todo_id):
        return 200, store.get(_id(todo_id))

    def _delete(self, store, raw, query, todo_id):
        store.delete(_id(todo_id))
        return 204, None

    def _complete(self, store, raw, query, todo_id):
        return 200, store.complete(_id(todo_id))

    def _add_tag(self, store, raw, query, todo_id):
        store.get(_id(todo_id))  # an unknown todo is reported before a bad body
        body = self._json_object(raw)
        return 200, store.add_tag(_id(todo_id), body.get("tag"))

    def _remove_tag(self, store, raw, query, todo_id, tag):
        return 200, store.remove_tag(_id(todo_id), unquote(tag))


class TodoServer(ThreadingHTTPServer):
    daemon_threads = True

    def __init__(self, address, store):
        self.store = store
        super().__init__(address, Handler)

    def server_close(self):
        super().server_close()
        self.store.close()


def make_server(db_path, host="127.0.0.1", port=0):
    """A bound, not yet serving HTTP server for the database at `db_path`."""
    store = TodoStore(db_path)
    try:
        return TodoServer((host, port), store)
    except BaseException:
        store.close()
        raise
