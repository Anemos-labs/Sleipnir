"""The storage layer: todos and their tags in an SQLite file."""

import re
import sqlite3
import threading


class TodoError(Exception):
    """Base class of the errors of this package."""


class NotFound(TodoError):
    def __init__(self):
        super().__init__("not found")


class ValidationError(TodoError):
    """An invalid title or tag; str(exc) is the message."""


_TAG = re.compile(r"[a-z0-9_-]{1,20}")
_MAX_ID = 2**63 - 1  # the largest id SQLite can hold; anything above is certainly unknown

_SCHEMA = """
CREATE TABLE IF NOT EXISTS todos (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,  -- AUTOINCREMENT: an id is never reused
    title    TEXT NOT NULL,
    done_seq INTEGER                             -- NULL while open, else the completion counter
);
CREATE TABLE IF NOT EXISTS tags (
    todo_id INTEGER NOT NULL,
    tag     TEXT NOT NULL,
    PRIMARY KEY (todo_id, tag)
);
"""


def normalize_tag(tag):
    """The normalised form of `tag`; ValidationError when it is not a valid tag."""
    if not isinstance(tag, str):
        raise ValidationError("invalid tag")
    tag = tag.strip().lower()
    if not _TAG.fullmatch(tag):
        raise ValidationError("invalid tag")
    return tag


class TodoStore:
    def __init__(self, path):
        # One connection, used from any thread, one statement sequence at a time.
        self._lock = threading.RLock()
        self._db = sqlite3.connect(path, check_same_thread=False)
        with self._lock, self._db:
            self._db.executescript(_SCHEMA)

    def close(self):
        with self._lock:
            self._db.close()

    # -- reading -----------------------------------------------------------------------------------------------

    def _load(self, todo_id):
        """The todo `todo_id` as a dict; NotFound when there is none. Call with the lock held."""
        if not isinstance(todo_id, int) or isinstance(todo_id, bool) or not 1 <= todo_id <= _MAX_ID:
            raise NotFound()
        row = self._db.execute("SELECT title, done_seq FROM todos WHERE id = ?", (todo_id,)).fetchone()
        if row is None:
            raise NotFound()
        tags = self._db.execute("SELECT tag FROM tags WHERE todo_id = ? ORDER BY tag", (todo_id,)).fetchall()
        return {"id": todo_id, "title": row[0], "done": row[1] is not None, "tags": [t for (t,) in tags]}

    def get(self, todo_id):
        with self._lock:
            return self._load(todo_id)

    def list(self, tag=None, done=None):
        wanted = None if tag is None else normalize_tag(tag)
        where, args = [], []
        if wanted is not None:
            where.append("id IN (SELECT todo_id FROM tags WHERE tag = ?)")
            args.append(wanted)
        if done is not None:
            where.append("done_seq IS NOT NULL" if done else "done_seq IS NULL")
        sql = "SELECT id FROM todos"
        if where:
            sql += " WHERE " + " AND ".join(where)
        sql += " ORDER BY done_seq IS NOT NULL, done_seq, id"  # open first; then by completion order
        with self._lock:
            ids = [row[0] for row in self._db.execute(sql, args)]
            return [self._load(i) for i in ids]

    # -- writing -----------------------------------------------------------------------------------------------

    def add(self, title, tags=()):
        if not isinstance(title, str) or not title.strip():
            raise ValidationError("title is required")
        if not isinstance(tags, (list, tuple, set, frozenset)):  # a bare str is not a list of tags
            raise ValidationError("invalid tag")
        wanted = sorted({normalize_tag(t) for t in tags})  # all checks happen before anything is written
        with self._lock, self._db:
            todo_id = self._db.execute("INSERT INTO todos (title) VALUES (?)", (title.strip(),)).lastrowid
            self._db.executemany("INSERT INTO tags (todo_id, tag) VALUES (?, ?)", [(todo_id, t) for t in wanted])
            return self._load(todo_id)

    def complete(self, todo_id):
        with self._lock, self._db:
            self._load(todo_id)
            self._db.execute(
                "UPDATE todos SET done_seq = (SELECT COALESCE(MAX(done_seq), 0) + 1 FROM todos) "
                "WHERE id = ? AND done_seq IS NULL",
                (todo_id,),
            )
            return self._load(todo_id)

    def delete(self, todo_id):
        with self._lock, self._db:
            self._load(todo_id)
            self._db.execute("DELETE FROM tags WHERE todo_id = ?", (todo_id,))
            self._db.execute("DELETE FROM todos WHERE id = ?", (todo_id,))

    def add_tag(self, todo_id, tag):
        with self._lock, self._db:
            self._load(todo_id)  # the todo is looked up before the tag is checked
            self._db.execute("INSERT OR IGNORE INTO tags (todo_id, tag) VALUES (?, ?)", (todo_id, normalize_tag(tag)))
            return self._load(todo_id)

    def remove_tag(self, todo_id, tag):
        with self._lock, self._db:
            self._load(todo_id)
            self._db.execute("DELETE FROM tags WHERE todo_id = ? AND tag = ?", (todo_id, normalize_tag(tag)))
            return self._load(todo_id)
