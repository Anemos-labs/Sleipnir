"""Two examples of README.md: the store used directly, and the server used over HTTP.

The acceptance tests that verify your work cover every rule of the README and start the server the same way as
`ExampleTests.test_http_roundtrip` does.
"""

import json
import os
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

from todo import NotFound, TodoStore, ValidationError
from todo.server import make_server

# Requests to 127.0.0.1 must not go through a proxy that the environment may configure.
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))


class ExampleTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.db = os.path.join(tmp.name, "todo.db")

    def test_store(self):
        store = TodoStore(self.db)
        self.addCleanup(store.close)
        first = store.add("  Buy milk ", tags=["Home", " shop", "home"])
        self.assertEqual(
            first, {"id": 1, "title": "Buy milk", "done": False, "tags": ["home", "shop"]}
        )
        store.add("Call Ann")
        store.add("Write report", tags=["work"])
        store.complete(3)
        store.complete(1)
        self.assertEqual([t["id"] for t in store.list()], [2, 3, 1])
        self.assertEqual([t["id"] for t in store.list(tag="HOME")], [1])
        store.delete(3)
        self.assertEqual(store.add("Next")["id"], 4)
        with self.assertRaises(ValidationError) as ctx:
            store.add("x", tags=["no good"])
        self.assertEqual(str(ctx.exception), "invalid tag")
        with self.assertRaises(NotFound):
            store.get(3)

    def test_http_roundtrip(self):
        server = make_server(self.db)
        # a short poll interval makes shutdown() return quickly (the default is half a second)
        thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        thread.start()

        def stop():
            server.shutdown()
            server.server_close()
            thread.join(10)

        self.addCleanup(stop)
        base = "http://127.0.0.1:%d" % server.server_address[1]

        def call(method, path, body=None):
            data = None if body is None else json.dumps(body).encode("utf-8")
            req = urllib.request.Request(base + path, data=data, method=method)
            try:
                with OPENER.open(req, timeout=10) as resp:
                    return resp.status, resp.read()
            except urllib.error.HTTPError as err:
                with err:
                    return err.code, err.read()

        status, raw = call("POST", "/todos", {"title": "Buy milk", "tags": ["Home"]})
        self.assertEqual(status, 201)
        self.assertEqual(
            json.loads(raw), {"id": 1, "title": "Buy milk", "done": False, "tags": ["home"]}
        )
        status, raw = call("POST", "/todos/1/complete")
        self.assertEqual(status, 200)
        self.assertIs(json.loads(raw)["done"], True)
        status, raw = call("GET", "/todos?done=maybe")
        self.assertEqual((status, json.loads(raw)), (400, {"error": "invalid query"}))
        status, raw = call("GET", "/todos/7")
        self.assertEqual((status, json.loads(raw)), (404, {"error": "not found"}))


if __name__ == "__main__":
    unittest.main()
