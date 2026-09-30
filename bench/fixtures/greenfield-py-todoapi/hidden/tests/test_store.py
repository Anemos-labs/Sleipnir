"""Acceptance tests for the storage layer. Every assertion follows from README.md."""

import os
import tempfile
import threading
import unittest

from todo import NotFound, TodoError, TodoStore, ValidationError


class StoreCase(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.path = os.path.join(tmp.name, "todo.db")
        self.store = self.open()

    def open(self):
        store = TodoStore(self.path)
        self.addCleanup(self._close, store)
        return store

    @staticmethod
    def _close(store):
        try:
            store.close()
        except Exception:
            pass

    def ids(self, todos):
        return [t["id"] for t in todos]

    def assertInvalid(self, message, func, *args, **kwargs):
        with self.assertRaises(ValidationError) as ctx:
            func(*args, **kwargs)
        self.assertEqual(str(ctx.exception), message)


class TodoShapeTests(StoreCase):
    def test_add_returns_the_todo(self):
        todo = self.store.add("Buy milk")
        self.assertEqual(todo, {"id": 1, "title": "Buy milk", "done": False, "tags": []})
        self.assertEqual(set(todo), {"id", "title", "done", "tags"})
        self.assertIs(todo["done"], False)
        self.assertIsInstance(todo["id"], int)
        self.assertIsInstance(todo["tags"], list)
        self.assertEqual(self.store.get(1), todo)

    def test_title_is_stripped_and_inner_whitespace_is_kept(self):
        self.assertEqual(self.store.add("  a  b \t\n")["title"], "a  b")

    def test_unicode_and_sql_characters_in_titles(self):
        for title in ["Café ☕ 日本語", "Robert'); DROP TABLE todos;--", 'say "hi" %s ? \\']:
            todo = self.store.add(title)
            self.assertEqual(self.store.get(todo["id"])["title"], title)
        self.assertEqual(len(self.store.list()), 3)

    def test_long_title(self):
        title = "x" * 20000
        self.assertEqual(self.store.get(self.store.add(title)["id"])["title"], title)

    def test_invalid_titles(self):
        for bad in ["", "   ", "\t\n ", None, 5, b"x", ["x"]]:
            with self.subTest(title=bad):
                self.assertInvalid("title is required", self.store.add, bad)

    def test_exceptions_are_todo_errors(self):
        self.assertTrue(issubclass(NotFound, TodoError))
        self.assertTrue(issubclass(ValidationError, TodoError))
        self.assertTrue(issubclass(TodoError, Exception))


class TagTests(StoreCase):
    def test_tags_are_normalised_deduplicated_and_sorted(self):
        todo = self.store.add("x", tags=["Work", " home ", "WORK", "a_b-c9"])
        self.assertEqual(todo["tags"], ["a_b-c9", "home", "work"])
        self.assertEqual(self.store.add("y", tags=("Z", "z", "y"))["tags"], ["y", "z"])
        self.assertEqual(self.store.add("s", tags={"B", "a"})["tags"], ["a", "b"])
        self.assertEqual(self.store.add("z", tags=[])["tags"], [])

    def test_single_character_tags(self):
        self.assertEqual(self.store.add("x", tags=["-", "_", "0", "a"])["tags"], ["-", "0", "_", "a"])

    def test_tag_length_limit(self):
        self.assertEqual(self.store.add("x", tags=["a" * 20])["tags"], ["a" * 20])
        self.assertEqual(self.store.add("x", tags=["  " + "B" * 20 + "  "])["tags"], ["b" * 20])
        self.assertInvalid("invalid tag", self.store.add, "x", tags=["a" * 21])

    def test_invalid_tags(self):
        for bad in ["", "  ", "a b", "x!", "café", "tag.", "a/b", "ü", "日本", "#x", "a,b"]:
            with self.subTest(tag=bad):
                self.assertInvalid("invalid tag", self.store.add, "x", tags=["ok", bad])
        self.assertEqual(self.store.list(), [])

    def test_tags_of_the_wrong_type(self):
        for bad in [None, 5, b"x", ["x"]]:
            with self.subTest(tag=bad):
                self.assertInvalid("invalid tag", self.store.add, "x", tags=[bad])
        self.assertInvalid("invalid tag", self.store.add, "x", tags="home")
        self.assertEqual(self.store.list(), [])

    def test_the_title_is_checked_before_the_tags(self):
        self.assertInvalid("title is required", self.store.add, "", tags=["bad tag"])
        self.assertInvalid("title is required", self.store.add, None, tags=[5])

    def test_failed_add_leaves_no_trace(self):
        self.assertInvalid("invalid tag", self.store.add, "x", tags=["ok", "bad tag!"])
        self.assertInvalid("title is required", self.store.add, "  ")
        self.assertEqual(self.store.list(), [])
        self.assertEqual(self.store.list(tag="ok"), [])
        todo = self.store.add("first")
        self.assertEqual(todo["id"], 1)
        self.assertEqual(todo["tags"], [])

    def test_add_tag(self):
        self.store.add("x", tags=["b"])
        todo = self.store.add_tag(1, " A ")
        self.assertEqual(todo, {"id": 1, "title": "x", "done": False, "tags": ["a", "b"]})
        self.assertEqual(self.store.get(1)["tags"], ["a", "b"])
        self.assertEqual(self.store.add_tag(1, "a")["tags"], ["a", "b"])  # adding twice changes nothing
        self.assertEqual(self.store.add_tag(1, "B")["tags"], ["a", "b"])

    def test_add_tag_errors(self):
        self.store.add("x")
        for bad in ["", "a b", "x!", "a" * 21, None, 5]:
            with self.subTest(tag=bad):
                self.assertInvalid("invalid tag", self.store.add_tag, 1, bad)
        self.assertEqual(self.store.get(1)["tags"], [])
        with self.assertRaises(NotFound):
            self.store.add_tag(2, "ok")

    def test_unknown_todo_is_reported_before_an_invalid_tag(self):
        with self.assertRaises(NotFound):
            self.store.add_tag(7, "bad tag!")
        with self.assertRaises(NotFound):
            self.store.remove_tag(7, "bad tag!")

    def test_remove_tag(self):
        self.store.add("x", tags=["a", "b", "c"])
        self.assertEqual(self.store.remove_tag(1, "B")["tags"], ["a", "c"])
        self.assertEqual(self.store.get(1)["tags"], ["a", "c"])
        self.assertEqual(self.store.remove_tag(1, "b")["tags"], ["a", "c"])  # not there: nothing changes
        self.assertEqual(self.store.remove_tag(1, "zzz")["tags"], ["a", "c"])
        self.assertInvalid("invalid tag", self.store.remove_tag, 1, "bad tag")
        self.assertInvalid("invalid tag", self.store.remove_tag, 1, None)
        self.assertEqual(self.store.get(1)["tags"], ["a", "c"])

    def test_tags_belong_to_their_todo(self):
        self.store.add("x", tags=["shared", "only-x"])
        self.store.add("y", tags=["shared"])
        self.store.remove_tag(1, "shared")
        self.assertEqual(self.ids(self.store.list(tag="shared")), [2])
        self.assertEqual(self.ids(self.store.list(tag="only-x")), [1])
        self.store.add_tag(2, "only-x")
        self.assertEqual(self.ids(self.store.list(tag="only-x")), [1, 2])


class IdTests(StoreCase):
    def test_ids_count_up_from_one(self):
        self.assertEqual([self.store.add(t)["id"] for t in "abc"], [1, 2, 3])

    def test_ids_are_never_reused(self):
        for title in "abc":
            self.store.add(title)
        self.store.delete(3)
        self.assertEqual(self.store.add("d")["id"], 4)
        for i in (1, 2, 4):
            self.store.delete(i)
        self.assertEqual(self.store.list(), [])
        self.assertEqual(self.store.add("e")["id"], 5)

    def test_get_and_not_found(self):
        self.store.add("x")
        self.assertEqual(self.store.get(1)["title"], "x")
        for unknown in [0, 2, -1, 10**30, 2**63, 2**64 + 1]:
            with self.subTest(todo_id=unknown):
                with self.assertRaises(NotFound) as ctx:
                    self.store.get(unknown)
                self.assertEqual(str(ctx.exception), "not found")
                with self.assertRaises(NotFound):
                    self.store.complete(unknown)
                with self.assertRaises(NotFound):
                    self.store.delete(unknown)
                with self.assertRaises(NotFound):
                    self.store.add_tag(unknown, "ok")
                with self.assertRaises(NotFound):
                    self.store.remove_tag(unknown, "ok")


class ListTests(StoreCase):
    def test_open_todos_first_then_done_in_completion_order(self):
        for title in "ABC":
            self.store.add(title)
        self.store.complete(3)
        self.store.complete(1)
        self.assertEqual(self.ids(self.store.list()), [2, 3, 1])
        self.store.complete(2)
        self.assertEqual(self.ids(self.store.list()), [3, 1, 2])

    def test_completing_again_keeps_the_place(self):
        for title in "ABC":
            self.store.add(title)
        self.store.complete(2)
        self.store.complete(1)
        self.store.complete(2)
        self.assertEqual(self.ids(self.store.list()), [3, 2, 1])
        self.assertEqual(self.ids(self.store.list(done=True)), [2, 1])

    def test_new_todos_go_before_the_done_ones(self):
        self.store.add("A")
        self.store.complete(1)
        self.store.add("B")
        self.store.add("C")
        self.assertEqual(self.ids(self.store.list()), [2, 3, 1])

    def test_order_survives_deleting_a_done_todo(self):
        for title in "ABCD":
            self.store.add(title)
        for i in (4, 2, 3):
            self.store.complete(i)
        self.store.delete(2)
        self.store.complete(1)
        self.assertEqual(self.ids(self.store.list()), [4, 3, 1])

    def test_done_filter(self):
        for title in "ABCD":
            self.store.add(title)
        self.store.complete(4)
        self.store.complete(2)
        self.assertEqual(self.ids(self.store.list(done=True)), [4, 2])
        self.assertEqual(self.ids(self.store.list(done=False)), [1, 3])
        self.assertEqual(self.ids(self.store.list(done=None)), [1, 3, 4, 2])
        self.assertEqual(self.ids(self.store.list()), [1, 3, 4, 2])

    def test_tag_filter(self):
        self.store.add("A", tags=["home"])
        self.store.add("B", tags=["work", "home"])
        self.store.add("C", tags=["work"])
        self.store.add("D")
        self.assertEqual(self.ids(self.store.list(tag="home")), [1, 2])
        self.assertEqual(self.ids(self.store.list(tag=" WORK ")), [2, 3])
        self.assertEqual(self.ids(self.store.list(tag="nothing")), [])
        self.assertEqual(self.ids(self.store.list(tag=None)), [1, 2, 3, 4])

    def test_both_filters(self):
        self.store.add("A", tags=["x"])
        self.store.add("B", tags=["x"])
        self.store.add("C", tags=["y"])
        self.store.add("D", tags=["x", "y"])
        self.store.complete(4)
        self.store.complete(1)
        self.assertEqual(self.ids(self.store.list(tag="x", done=True)), [4, 1])
        self.assertEqual(self.ids(self.store.list(tag="x", done=False)), [2])
        self.assertEqual(self.ids(self.store.list(tag="y", done=True)), [4])
        self.assertEqual(self.ids(self.store.list(tag="y", done=False)), [3])
        self.assertEqual(self.ids(self.store.list(tag="x")), [2, 4, 1])

    def test_an_invalid_tag_filter_is_an_error(self):
        for bad in ["", "a b", "x!", "a" * 21, 5]:
            with self.subTest(tag=bad):
                self.assertInvalid("invalid tag", self.store.list, tag=bad)

    def test_list_items_are_full_todos(self):
        self.store.add("A", tags=["b", "a"])
        self.store.complete(1)
        self.assertEqual(self.store.list(), [{"id": 1, "title": "A", "done": True, "tags": ["a", "b"]}])
        self.assertIs(self.store.list()[0]["done"], True)


class MutationTests(StoreCase):
    def test_complete(self):
        self.store.add("x", tags=["t"])
        todo = self.store.complete(1)
        self.assertEqual(todo, {"id": 1, "title": "x", "done": True, "tags": ["t"]})
        self.assertIs(todo["done"], True)
        self.assertEqual(self.store.complete(1), todo)  # again: unchanged
        self.assertEqual(self.store.get(1), todo)

    def test_delete(self):
        self.store.add("x", tags=["t"])
        self.store.add("y", tags=["t"])
        self.assertIsNone(self.store.delete(1))
        with self.assertRaises(NotFound):
            self.store.get(1)
        with self.assertRaises(NotFound):
            self.store.delete(1)
        self.assertEqual(self.ids(self.store.list()), [2])
        self.assertEqual(self.ids(self.store.list(tag="t")), [2])
        self.assertEqual(self.store.get(2)["tags"], ["t"])


class PersistenceTests(StoreCase):
    def test_everything_survives_close_and_reopen(self):
        self.store.add("A", tags=["x", "y"])
        self.store.add("B")
        self.store.add("C", tags=["x"])
        self.store.complete(3)
        self.store.complete(1)
        self.store.add_tag(2, "late")
        self.store.delete(2)
        self.store.add("D", tags=["Z"])
        before = self.store.list()
        self.assertEqual(self.ids(before), [4, 3, 1])
        self.store.close()

        again = self.open()
        self.assertEqual(again.list(), before)
        self.assertEqual(again.get(3)["done"], True)
        self.assertEqual(again.list(tag="x", done=True), [before[1], before[2]])
        self.assertEqual(again.add("E")["id"], 5)  # the id counter went on, 2 was not reused either
        self.assertEqual(self.ids(again.list()), [4, 5, 3, 1])

    def test_ids_continue_after_the_last_todo_was_deleted(self):
        self.store.add("A")
        self.store.add("B")
        self.store.delete(2)
        self.store.close()
        again = self.open()
        self.assertEqual(again.add("C")["id"], 3)

    def test_an_empty_database_is_fine(self):
        self.store.close()
        again = self.open()
        self.assertEqual(again.list(), [])
        self.assertEqual(again.add("x")["id"], 1)

    def test_unicode_survives_reopening(self):
        self.store.add("Café ☕", tags=["ok"])
        self.store.close()
        self.assertEqual(self.open().get(1)["title"], "Café ☕")


class ThreadTests(StoreCase):
    def run_in_thread(self, func):
        result = {}

        def target():
            try:
                result["value"] = func()
            except BaseException as exc:  # reported in the main thread
                result["error"] = exc

        thread = threading.Thread(target=target)
        thread.start()
        thread.join(30)
        self.assertFalse(thread.is_alive(), "the thread did not finish")
        if "error" in result:
            raise result["error"]
        return result["value"]

    def test_a_store_made_in_one_thread_works_in_another(self):
        def work():
            todo = self.store.add("from a thread", tags=["t"])
            self.store.complete(todo["id"])
            return self.store.list(), self.store.get(1)

        listed, got = self.run_in_thread(work)
        self.assertEqual(listed, [{"id": 1, "title": "from a thread", "done": True, "tags": ["t"]}])
        self.assertEqual(got, listed[0])
        self.assertEqual(self.store.get(1), got)  # and back in the creating thread

    def test_errors_cross_threads_too(self):
        def work():
            with self.assertRaises(NotFound):
                self.store.get(99)
            with self.assertRaises(ValidationError):
                self.store.add(" ")

        self.run_in_thread(work)

    def test_concurrent_writers_and_readers(self):
        workers, each = 8, 15
        errors = []
        created = []
        lock = threading.Lock()
        barrier = threading.Barrier(workers + 1)

        def writer(n):
            try:
                barrier.wait(30)
                for i in range(each):
                    todo = self.store.add("w%d-%d" % (n, i), tags=["w%d" % n, "all"])
                    with lock:
                        created.append(todo["id"])
                    if i % 3 == 0:
                        self.store.complete(todo["id"])
            except BaseException as exc:
                errors.append(exc)

        def reader():
            try:
                barrier.wait(30)
                for _ in range(20):
                    self.store.list(tag="all")
                    self.store.list(done=True)
            except BaseException as exc:
                errors.append(exc)

        threads = [threading.Thread(target=writer, args=(n,)) for n in range(workers)]
        threads.append(threading.Thread(target=reader))
        for t in threads:
            t.start()
        for t in threads:
            t.join(60)
            self.assertFalse(t.is_alive())
        self.assertEqual(errors, [])
        self.assertEqual(sorted(created), list(range(1, workers * each + 1)))
        everything = self.store.list()
        self.assertEqual(len(everything), workers * each)
        self.assertEqual(len(self.store.list(tag="all")), workers * each)
        self.assertEqual(len(self.store.list(tag="w3")), each)
        self.assertEqual(len(self.store.list(done=True)), workers * 5)
        # open todos come first, in id order
        open_ids = [t["id"] for t in everything if not t["done"]]
        self.assertEqual(open_ids, sorted(open_ids))
        self.assertEqual([t["done"] for t in everything], sorted(t["done"] for t in everything))


if __name__ == "__main__":
    unittest.main()
