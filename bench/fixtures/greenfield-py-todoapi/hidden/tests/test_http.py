"""Acceptance tests for the HTTP layer. Every assertion follows from README.md."""

import json
import os
import tempfile
import threading
import unittest
import urllib.error
import urllib.request

from todo import TodoStore
from todo.server import make_server

# Never go through a proxy that the environment may configure: the server is on 127.0.0.1.
OPENER = urllib.request.build_opener(urllib.request.ProxyHandler({}))

NOT_FOUND = {"error": "not found"}
NOT_ALLOWED = {"error": "method not allowed"}


class Response:
    def __init__(self, status, headers, raw):
        self.status = status
        self.headers = headers
        self.raw = raw

    @property
    def json(self):
        return json.loads(self.raw.decode("utf-8"))

    def todo_ids(self):
        return [t["id"] for t in self.json]


class ServerCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = os.path.join(tmp.name, "todo.db")
        self.server = None
        self.start()

    def start(self):
        self.server = make_server(self.path)
        # a short poll interval makes shutdown() return quickly (the default is half a second)
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={"poll_interval": 0.01}, daemon=True)
        self.thread.start()
        self.base = "http://127.0.0.1:%d" % self.server.server_address[1]
        self.addCleanup(self.stop)

    def stop(self):
        if self.server is not None:
            server, self.server = self.server, None
            server.shutdown()
            server.server_close()
            self.thread.join(10)

    def call(self, method, path, body=None, raw=None):
        """One request. `body` is encoded as JSON; `raw` is sent as it is. Returns a Response."""
        data = raw
        if body is not None:
            data = json.dumps(body).encode("utf-8")
        headers = {"Content-Type": "application/json"} if data is not None else {}
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=headers)
        try:
            with OPENER.open(req, timeout=15) as resp:
                return Response(resp.status, resp.headers, resp.read())
        except urllib.error.HTTPError as err:
            with err:
                return Response(err.code, err.headers, err.read())

    def create(self, title, tags=None):
        body = {"title": title}
        if tags is not None:
            body["tags"] = tags
        resp = self.call("POST", "/todos", body)
        self.assertEqual(resp.status, 201, resp.raw)
        return resp.json

    def assertError(self, resp, status, message):
        self.assertEqual((resp.status, resp.json), (status, {"error": message}), resp.raw)
        self.assertEqual(resp.headers.get_content_type(), "application/json")


class CreateTests(ServerCase):
    def test_create(self):
        resp = self.call("POST", "/todos", {"title": "Buy milk", "tags": ["Home", "shop"]})
        self.assertEqual(resp.status, 201)
        self.assertEqual(resp.headers.get_content_type(), "application/json")
        self.assertEqual(resp.json, {"id": 1, "title": "Buy milk", "done": False, "tags": ["home", "shop"]})
        self.assertIs(resp.json["done"], False)
        self.assertEqual(self.call("GET", "/todos/1").json, resp.json)

    def test_create_strips_and_normalises(self):
        todo = self.create("  spaced out  ", [" B ", "a", "B"])
        self.assertEqual(todo, {"id": 1, "title": "spaced out", "done": False, "tags": ["a", "b"]})

    def test_tags_are_optional(self):
        self.assertEqual(self.create("x")["tags"], [])
        self.assertEqual(self.create("y", [])["tags"], [])

    def test_unknown_fields_are_ignored(self):
        resp = self.call("POST", "/todos", {"title": "x", "done": True, "id": 99, "extra": [1]})
        self.assertEqual(resp.status, 201)
        self.assertEqual(resp.json, {"id": 1, "title": "x", "done": False, "tags": []})

    def test_unicode_and_large_titles(self):
        title = "Café ☕ 日本語 \U0001f600"
        self.assertEqual(self.create(title)["title"], title)
        self.assertEqual(self.call("GET", "/todos/1").json["title"], title)
        self.assertEqual(self.call("GET", "/todos").json[0]["title"], title)
        big = "x" * 50000
        self.assertEqual(self.create(big)["title"], big)
        self.assertEqual(self.call("GET", "/todos/2").json["title"], big)

    def test_invalid_json(self):
        bodies = [b"", b"not json", b"{", b"[]", b'"x"', b"3", b"null", b"true", b"\xff\xfe", b'{"title": "x"']
        for raw in bodies:
            with self.subTest(body=raw):
                self.assertError(self.call("POST", "/todos", raw=raw), 400, "invalid json")
        self.assertEqual(self.call("GET", "/todos").json, [])

    def test_title_errors(self):
        for body in [{}, {"title": None}, {"title": 5}, {"title": ""}, {"title": "   "}, {"title": ["x"]}, {"tags": ["a"]}]:
            with self.subTest(body=body):
                self.assertError(self.call("POST", "/todos", body), 400, "title is required")

    def test_tag_errors(self):
        bad_tags = ["home", 5, [1], ["bad tag"], [""], ["a" * 21], {"a": 1}, [["a"]], ["ok", None]]
        for tags in bad_tags:
            with self.subTest(tags=tags):
                self.assertError(self.call("POST", "/todos", {"title": "x", "tags": tags}), 400, "invalid tag")

    def test_the_title_is_reported_before_the_tags(self):
        self.assertError(self.call("POST", "/todos", {"title": " ", "tags": ["!"]}), 400, "title is required")

    def test_failed_creations_leave_no_trace(self):
        self.call("POST", "/todos", {"title": "x", "tags": ["ok", "bad tag"]})
        self.call("POST", "/todos", {"title": ""})
        self.call("POST", "/todos", raw=b"oops")
        self.assertEqual(self.call("GET", "/todos").json, [])
        self.assertEqual(self.create("first")["id"], 1)


class ReadTests(ServerCase):
    def test_get(self):
        self.create("x", ["a"])
        resp = self.call("GET", "/todos/1")
        self.assertEqual(resp.status, 200)
        self.assertEqual(resp.headers.get_content_type(), "application/json")
        self.assertEqual(resp.json, {"id": 1, "title": "x", "done": False, "tags": ["a"]})

    def test_get_unknown(self):
        self.create("x")
        for path in ["/todos/2", "/todos/0", "/todos/99999999999999999999", "/todos/" + "9" * 400]:
            with self.subTest(path=path[:40]):
                self.assertError(self.call("GET", path), 404, "not found")

    def test_empty_list(self):
        resp = self.call("GET", "/todos")
        self.assertEqual((resp.status, resp.json), (200, []))
        self.assertEqual(resp.headers.get_content_type(), "application/json")

    def test_list_order(self):
        for title in "ABC":
            self.create(title)
        self.call("POST", "/todos/3/complete")
        self.call("POST", "/todos/1/complete")
        resp = self.call("GET", "/todos")
        self.assertEqual(resp.todo_ids(), [2, 3, 1])
        self.assertEqual([t["done"] for t in resp.json], [False, True, True])
        self.assertEqual(resp.json[0], {"id": 2, "title": "B", "done": False, "tags": []})

    def test_list_filters(self):
        self.create("A", ["home"])
        self.create("B", ["work", "home"])
        self.create("C", ["work"])
        self.create("D")
        self.call("POST", "/todos/2/complete")
        self.call("POST", "/todos/1/complete")
        ids = lambda query: self.call("GET", "/todos" + query).todo_ids()
        self.assertEqual(ids(""), [3, 4, 2, 1])
        self.assertEqual(ids("?done=true"), [2, 1])
        self.assertEqual(ids("?done=false"), [3, 4])
        self.assertEqual(ids("?tag=home"), [2, 1])
        self.assertEqual(ids("?tag=HOME"), [2, 1])
        self.assertEqual(ids("?tag=work"), [3, 2])
        self.assertEqual(ids("?tag=nothing"), [])
        self.assertEqual(ids("?tag=work&done=false"), [3])
        self.assertEqual(ids("?done=true&tag=home"), [2, 1])
        self.assertEqual(ids("?done=false&tag=home"), [])
        self.assertEqual(ids("?other=1"), [3, 4, 2, 1])
        self.assertEqual(ids("?tag=work&other=1"), [3, 2])

    def test_list_query_errors(self):
        for query in ["?done=maybe", "?done=", "?done=TRUE", "?done=1", "?done=True", "?done=false%20"]:
            with self.subTest(query=query):
                self.assertError(self.call("GET", "/todos" + query), 400, "invalid query")
        for query in ["?tag=", "?tag=Bad%20Tag", "?tag=x!", "?tag=%20", "?tag=" + "a" * 21]:
            with self.subTest(query=query):
                self.assertError(self.call("GET", "/todos" + query), 400, "invalid tag")

    def test_done_is_checked_before_the_tag(self):
        self.assertError(self.call("GET", "/todos?tag=bad%20tag&done=maybe"), 400, "invalid query")
        self.assertError(self.call("GET", "/todos?done=maybe&tag="), 400, "invalid query")


class ChangeTests(ServerCase):
    def test_complete(self):
        self.create("x", ["t"])
        resp = self.call("POST", "/todos/1/complete")
        self.assertEqual(resp.status, 200)
        self.assertEqual(resp.headers.get_content_type(), "application/json")
        self.assertEqual(resp.json, {"id": 1, "title": "x", "done": True, "tags": ["t"]})
        self.assertIs(resp.json["done"], True)
        again = self.call("POST", "/todos/1/complete")
        self.assertEqual((again.status, again.json), (200, resp.json))
        self.assertEqual(self.call("GET", "/todos/1").json, resp.json)

    def test_complete_ignores_the_body(self):
        self.create("x")
        for raw in [b"", b"not json", b'{"done": false}']:
            with self.subTest(body=raw):
                resp = self.call("POST", "/todos/1/complete", raw=raw)
                self.assertEqual((resp.status, resp.json["done"]), (200, True))

    def test_complete_unknown(self):
        self.assertError(self.call("POST", "/todos/5/complete"), 404, "not found")

    def test_completing_again_keeps_the_place_in_the_list(self):
        for title in "ABC":
            self.create(title)
        for i in (2, 1, 2):
            self.call("POST", "/todos/%d/complete" % i)
        self.assertEqual(self.call("GET", "/todos").todo_ids(), [3, 2, 1])

    def test_delete(self):
        self.create("x", ["t"])
        self.create("y", ["t"])
        resp = self.call("DELETE", "/todos/1")
        self.assertEqual((resp.status, resp.raw), (204, b""))
        self.assertError(self.call("GET", "/todos/1"), 404, "not found")
        self.assertError(self.call("DELETE", "/todos/1"), 404, "not found")
        self.assertEqual(self.call("GET", "/todos").todo_ids(), [2])
        self.assertEqual(self.call("GET", "/todos?tag=t").todo_ids(), [2])
        self.assertEqual(self.create("z")["id"], 3)

    def test_delete_unknown(self):
        self.assertError(self.call("DELETE", "/todos/9"), 404, "not found")

    def test_add_tag(self):
        self.create("x", ["b"])
        resp = self.call("POST", "/todos/1/tags", {"tag": " A "})
        self.assertEqual(resp.status, 200)
        self.assertEqual(resp.headers.get_content_type(), "application/json")
        self.assertEqual(resp.json, {"id": 1, "title": "x", "done": False, "tags": ["a", "b"]})
        again = self.call("POST", "/todos/1/tags", {"tag": "a"})
        self.assertEqual((again.status, again.json["tags"]), (200, ["a", "b"]))
        self.assertEqual(self.call("GET", "/todos/1").json["tags"], ["a", "b"])
        self.assertEqual(self.call("GET", "/todos?tag=a").todo_ids(), [1])

    def test_add_tag_errors(self):
        self.create("x")
        for raw in [b"", b"nope", b"[]", b'"a"', b"null"]:
            with self.subTest(body=raw):
                self.assertError(self.call("POST", "/todos/1/tags", raw=raw), 400, "invalid json")
        for body in [{}, {"tag": None}, {"tag": 5}, {"tag": ""}, {"tag": "bad tag"}, {"tag": "a" * 21}, {"tag": ["a"]}, {"tags": ["a"]}]:
            with self.subTest(body=body):
                self.assertError(self.call("POST", "/todos/1/tags", body), 400, "invalid tag")
        self.assertEqual(self.call("GET", "/todos/1").json["tags"], [])

    def test_the_todo_is_checked_before_the_body(self):
        self.assertError(self.call("POST", "/todos/9/tags", {"tag": "ok"}), 404, "not found")
        self.assertError(self.call("POST", "/todos/9/tags", raw=b"not json"), 404, "not found")
        self.assertError(self.call("POST", "/todos/9/tags", {"tag": "bad tag"}), 404, "not found")

    def test_remove_tag(self):
        self.create("x", ["a", "b", "c"])
        resp = self.call("DELETE", "/todos/1/tags/b")
        self.assertEqual(resp.status, 200)
        self.assertEqual(resp.headers.get_content_type(), "application/json")
        self.assertEqual(resp.json, {"id": 1, "title": "x", "done": False, "tags": ["a", "c"]})
        self.assertEqual(self.call("DELETE", "/todos/1/tags/b").json["tags"], ["a", "c"])  # absent: no change
        self.assertEqual(self.call("DELETE", "/todos/1/tags/zzz").json["tags"], ["a", "c"])
        self.assertEqual(self.call("DELETE", "/todos/1/tags/C").json["tags"], ["a"])  # case-insensitive
        self.assertEqual(self.call("DELETE", "/todos/1/tags/%41").json["tags"], [])  # percent-decoded: "A"

    def test_remove_tag_errors(self):
        self.create("x", ["a"])
        for tag in ["bad%20tag", "x!", "a" * 21, "%20"]:
            with self.subTest(tag=tag):
                self.assertError(self.call("DELETE", "/todos/1/tags/" + tag), 400, "invalid tag")
        self.assertError(self.call("DELETE", "/todos/9/tags/a"), 404, "not found")
        self.assertError(self.call("DELETE", "/todos/9/tags/bad%20tag"), 404, "not found")
        self.assertEqual(self.call("GET", "/todos/1").json["tags"], ["a"])


class RoutingTests(ServerCase):
    def test_unknown_routes(self):
        self.create("x")
        paths = ["/", "/nope", "/todo", "/todos/", "/todos/1/", "/todos/1/unknown", "/todos/abc", "/todos/abc/complete",
                 "/todos//tags", "/todos/1/complete/", "/todos/-1", "/todos/1.5", "/todos/1/tags/a/b", "/todos/1/tags/"]
        for path in paths:
            with self.subTest(path=path):
                self.assertError(self.call("GET", path), 404, "not found")

    def test_unknown_route_with_another_method(self):
        for method in ["POST", "PUT", "DELETE", "PATCH"]:
            with self.subTest(method=method):
                self.assertError(self.call(method, "/nope"), 404, "not found")
                self.assertError(self.call(method, "/todos/abc"), 404, "not found")

    def test_methods_that_do_not_belong_to_a_route(self):
        self.create("x")
        cases = [
            ("PUT", "/todos"), ("DELETE", "/todos"), ("PATCH", "/todos"),
            ("POST", "/todos/1"), ("PUT", "/todos/1"), ("PATCH", "/todos/1"),
            ("GET", "/todos/1/complete"), ("PUT", "/todos/1/complete"), ("DELETE", "/todos/1/complete"),
            ("GET", "/todos/1/tags"), ("PUT", "/todos/1/tags"), ("DELETE", "/todos/1/tags"),
            ("GET", "/todos/1/tags/a"), ("POST", "/todos/1/tags/a"), ("PUT", "/todos/1/tags/a"),
        ]
        for method, path in cases:
            with self.subTest(method=method, path=path):
                resp = self.call(method, path)
                self.assertEqual((resp.status, resp.json), (405, NOT_ALLOWED))
                self.assertEqual(resp.headers.get_content_type(), "application/json")
        self.assertEqual(self.call("GET", "/todos").todo_ids(), [1])  # nothing was changed

    def test_the_method_is_checked_before_the_todo(self):
        self.assertError(self.call("PATCH", "/todos/999"), 405, "method not allowed")
        self.assertError(self.call("POST", "/todos/999"), 405, "method not allowed")
        self.assertError(self.call("GET", "/todos/999/complete"), 405, "method not allowed")

    def test_query_strings_do_not_change_routing(self):
        self.create("x")
        self.assertEqual(self.call("GET", "/todos/1?x=1").json["id"], 1)
        self.assertEqual(self.call("POST", "/todos/1/complete?x=1").status, 200)


class PersistenceAndConcurrencyTests(ServerCase):
    def test_data_survives_a_restart(self):
        self.create("A", ["x"])
        self.create("B")
        self.call("POST", "/todos/2/complete")
        self.call("POST", "/todos/1/tags", {"tag": "y"})
        before = self.call("GET", "/todos").json
        self.assertEqual([t["id"] for t in before], [1, 2])
        self.stop()

        self.start()
        self.assertEqual(self.call("GET", "/todos").json, before)
        self.assertEqual(self.create("C")["id"], 3)
        self.call("DELETE", "/todos/3")
        self.stop()

        self.start()
        self.assertEqual(self.create("D")["id"], 4)

    def test_the_database_is_closed_with_the_server(self):
        self.create("A", ["x"])
        self.call("POST", "/todos/1/complete")
        self.stop()
        store = TodoStore(self.path)
        try:
            self.assertEqual(store.list(), [{"id": 1, "title": "A", "done": True, "tags": ["x"]}])
            store.add("B")
        finally:
            store.close()
        self.start()
        self.assertEqual(self.call("GET", "/todos").todo_ids(), [2, 1])

    def test_concurrent_requests(self):
        workers, each = 4, 6
        ids, errors = [], []
        lock = threading.Lock()

        def work(n):
            try:
                for i in range(each):
                    resp = self.call("POST", "/todos", {"title": "w%d-%d" % (n, i), "tags": ["all"]})
                    if resp.status != 201:
                        raise AssertionError((resp.status, resp.raw))
                    with lock:
                        ids.append(resp.json["id"])
                    self.call("GET", "/todos?tag=all")
            except BaseException as exc:
                errors.append(exc)

        threads = [threading.Thread(target=work, args=(n,)) for n in range(workers)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(60)
            self.assertFalse(t.is_alive())
        self.assertEqual(errors, [])
        self.assertEqual(sorted(ids), list(range(1, workers * each + 1)))
        self.assertEqual(len(self.call("GET", "/todos?tag=all").json), workers * each)


if __name__ == "__main__":
    unittest.main()
