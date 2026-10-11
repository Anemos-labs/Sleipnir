"""The Python side of sleipnir's rebuild tasks (taskgen.Rebuild).

Reads one JSON request from stdin and writes one JSON answer to stdout. It only parses the sources it is given (with
the ast module); it imports and runs none of them. Standard library only, Python 3.8 or later.

Request:
  {"modules": [{"name": "pkg.core", "path": "src/pkg/core.py", "package": false, "source": "..."}],
   "tests":   [{"path": "tests/test_core.py", "package": "", "source": "..."}]}

  A module's "package" is true for a package's __init__.py. A test's "package" is the dotted name of the package its
  directory is (for the relative imports of a test inside a package), or "".

Answer:
  {"modules": {"pkg.core": {"stub": "...", "doc": "...", "api": [entry, ...], "imports": ["pkg.util"]}},
   "tests":   {"tests/test_core.py": {"refs": {"pkg.core": 3}, "names": {"pkg.core": ["Stack", "_helper"]}}},
   "errors":  ["src/pkg/broken.py: invalid syntax (line 3)"]}

  stub     the module with the body of every function and method (and of an `if __name__ == "__main__":` block)
           replaced by `raise NotImplementedError`, keeping each docstring; a body that is already only `pass`, `...`
           or a raise of NotImplementedError is kept. Everything else (imports, assignments, class attributes,
           decorators, comments outside the bodies) is kept as it is.
  api      the public API: functions, classes (with their public and special methods and attributes), assignments
           and, in a package's __init__.py, the imports of in-scope modules; "tests" marks a name documented only
           because the tests use it. Public is __all__ when the module defines it, else every name without a
           leading underscore.
  imports  the in-scope modules the module imports, anywhere in it.
  refs     how often a test file refers to each in-scope module: an import of it, or a name imported from it or read
           as an attribute of it, followed through the re-exports of a package's __init__.py to the module that
           defines the name.
  names    the names a test file uses of each module.
"""

import ast
import bisect
import copy
import json
import sys

STUB = b"raise NotImplementedError"
MAX_VALUE = 240  # longest assignment shown whole in the API


def line_offsets(data):
    """Byte offset of the start of every line."""
    offs = [0]
    i = data.find(b"\n")
    while i >= 0:
        offs.append(i + 1)
        i = data.find(b"\n", i + 1)
    return offs


def is_docstring(stmt):
    return isinstance(stmt, ast.Expr) and isinstance(stmt.value, ast.Constant) and isinstance(stmt.value.value, str)


def is_trivial(stmts):
    """A body with nothing to implement: pass, ..., or a raise of NotImplementedError."""
    if len(stmts) != 1:
        return False
    s = stmts[0]
    if isinstance(s, ast.Pass):
        return True
    if isinstance(s, ast.Expr) and isinstance(s.value, ast.Constant) and s.value.value is Ellipsis:
        return True
    if isinstance(s, ast.Raise) and s.exc is not None:
        e = s.exc.func if isinstance(s.exc, ast.Call) else s.exc
        return isinstance(e, ast.Name) and e.id == "NotImplementedError"
    return False


def is_main_guard(s):
    t = s.test
    return (isinstance(t, ast.Compare) and isinstance(t.left, ast.Name) and t.left.id == "__name__"
            and len(t.comparators) == 1 and isinstance(t.comparators[0], ast.Constant)
            and t.comparators[0].value == "__main__")


def try_types():
    out = [ast.Try]
    if hasattr(ast, "TryStar"):
        out.append(ast.TryStar)
    return tuple(out)


TRY = try_types()


class Source:
    """A module's text as bytes, with positions in it."""

    def __init__(self, text):
        self.text = text
        self.data = text.encode("utf-8")
        self.offs = line_offsets(self.data)

    def pos(self, lineno, col):
        return self.offs[lineno - 1] + col

    def start(self, stmt):
        """Where a statement's text starts: at the @ of its first decorator for a decorated definition."""
        decs = getattr(stmt, "decorator_list", None)
        if decs:
            d = decs[0]
            p = self.pos(d.lineno, d.col_offset)
            at = self.data.rfind(b"@", self.offs[d.lineno - 1], p)
            return at if at >= 0 else p
        return self.pos(stmt.lineno, stmt.col_offset)

    def end(self, stmt):
        return self.pos(stmt.end_lineno, stmt.end_col_offset)

    def segment(self, stmt):
        return self.data[self.start(stmt):self.end(stmt)].decode("utf-8")


# ---- stubs ----

def is_comment_line(line):
    s = line.strip()
    return not s or s.startswith(b"#")


def indent_of(line):
    return len(line) - len(line.lstrip(b" \t"))


class Stubber:
    def __init__(self, src):
        self.src = src
        self.edits = []

    def line(self, n):
        """Line n (1-based) without its line break."""
        src = self.src
        end = src.offs[n] - 1 if n < len(src.offs) else len(src.data)
        return src.data[src.offs[n - 1]:end].rstrip(b"\r")

    def replace(self, stmts, after):
        """Replace a body's statements with the stub. When the body starts a line of its own, the comment lines just
        above it (below line after: the def line or the docstring's last line) and those just below it, indented as
        deep, go with it."""
        src = self.src
        start, end = src.start(stmts[0]), src.end(stmts[-1])
        stub = STUB
        last = stmts[-1].end_lineno
        tail = self.line(last)[end - src.offs[last - 1]:]
        if tail.strip().startswith(b"#"):
            end += len(tail)  # a comment after the last statement, on its line
        n = bisect.bisect_right(src.offs, start)  # the line start is on
        lead = src.data[src.offs[n - 1]:start]
        if lead.strip() == b"":
            while n - 1 > after and is_comment_line(self.line(n - 1)):
                n -= 1
            start, stub = src.offs[n - 1], lead + STUB
            n = last
            while n < len(src.offs):
                nxt = self.line(n + 1)
                if not nxt.strip().startswith(b"#") or indent_of(nxt) < len(lead):
                    break
                n += 1
                end = src.offs[n - 1] + len(nxt)
        self.edits.append((start, end, stub))

    def function(self, f):
        body = f.body
        after = f.lineno
        rest = body
        if body and is_docstring(body[0]):
            rest, after = body[1:], body[0].end_lineno
        if rest and not is_trivial(rest):
            self.replace(rest, after)

    def block(self, stmts, module_level):
        for s in stmts:
            if isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef)):
                self.function(s)
            elif isinstance(s, ast.ClassDef):
                self.block(s.body, False)
            elif isinstance(s, ast.If):
                if module_level and is_main_guard(s):
                    if not is_trivial(s.body):
                        self.replace(s.body, s.lineno)
                    self.block(s.orelse, module_level)
                else:
                    self.block(s.body, module_level)
                    self.block(s.orelse, module_level)
            elif isinstance(s, TRY):
                self.block(s.body, module_level)
                for h in s.handlers:
                    self.block(h.body, module_level)
                self.block(s.orelse, module_level)
                self.block(s.finalbody, module_level)
            elif isinstance(s, (ast.With, ast.AsyncWith)):
                self.block(s.body, module_level)

    def result(self):
        out = bytearray(self.src.data)
        for start, end, stub in sorted(self.edits, reverse=True):
            out[start:end] = stub
        return bytes(out).decode("utf-8")


def stub_module(tree, src, path):
    st = Stubber(src)
    st.block(tree.body, True)
    out = st.result()
    ast.parse(out, filename=path)  # the stub must still be valid Python
    return out


# ---- API ----

def all_names(tree):
    """The module's __all__, when it is a literal list or tuple of strings."""
    for s in tree.body:
        if isinstance(s, (ast.Assign, ast.AnnAssign)):
            targets = s.targets if isinstance(s, ast.Assign) else [s.target]
            if any(isinstance(t, ast.Name) and t.id == "__all__" for t in targets):
                v = s.value
                if isinstance(v, (ast.List, ast.Tuple)) and all(
                        isinstance(e, ast.Constant) and isinstance(e.value, str) for e in v.elts):
                    return set(e.value for e in v.elts)
    return None


def header(node, src):
    """The definition's decorators and its def or class line, without the colon."""
    if hasattr(ast, "unparse"):
        c = copy.copy(node)
        c.body = [ast.Pass()]
        text = ast.unparse(c)
        head = text.rsplit("\n", 1)[0]
        return head[:-1] if head.endswith(":") else head
    start = src.start(node)
    first = node.body[0]
    end = src.pos(first.lineno, first.col_offset)
    text = src.data[start:end].decode("utf-8").rstrip()
    if text.endswith(":"):
        text = text[:-1]
    return text.rstrip()


def assign_names(s):
    targets = s.targets if isinstance(s, ast.Assign) else [s.target]
    out = []
    for t in targets:
        for n in ast.walk(t):
            if isinstance(n, ast.Name):
                out.append(n.id)
    return out


def assign_code(s, src):
    text = src.segment(s)
    if len(text) <= MAX_VALUE:
        return text
    if isinstance(s, ast.AnnAssign):
        return src.segment(s.target) + ": " + src.segment(s.annotation) + (" = ..." if s.value is not None else "")
    return " = ".join(src.segment(t) for t in s.targets) + " = ..."


def entry(kind, name, code, doc, tests=False, members=None):
    e = {"kind": kind, "name": name, "code": code, "doc": doc or ""}
    if tests:
        e["tests"] = True
    if members:
        e["members"] = members
    return e


def is_special(name):
    return name.startswith("__") and name.endswith("__")


def class_entry(c, src, used, tests):
    members = []
    for s in c.body:
        if isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef)):
            if not s.name.startswith("_") or is_special(s.name) or s.name in used:
                members.append(entry("def", s.name, header(s, src), ast.get_docstring(s)))
        elif isinstance(s, ast.ClassDef):
            if not s.name.startswith("_") or s.name in used:
                members.append(entry("class", s.name, header(s, src), ast.get_docstring(s)))
        elif isinstance(s, (ast.Assign, ast.AnnAssign)):
            names = assign_names(s)
            if any(not n.startswith("_") or n in used for n in names):
                members.append(entry("var", ",".join(names), assign_code(s, src), ""))
    return entry("class", c.name, header(c, src), ast.get_docstring(c), tests, members)


def module_api(tree, src, mod, modules, used):
    """The public API of a module, and what of the rest the tests use."""
    public = all_names(tree)

    def shown(name):
        if public is not None:
            return name in public
        return not name.startswith("_")

    out = []

    def visit(stmts):
        for s in stmts:
            if isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef)):
                if shown(s.name) or s.name in used:
                    out.append(entry("def", s.name, header(s, src), ast.get_docstring(s), not shown(s.name)))
            elif isinstance(s, ast.ClassDef):
                if shown(s.name) or s.name in used:
                    out.append(class_entry(s, src, used, not shown(s.name)))
            elif isinstance(s, (ast.Assign, ast.AnnAssign)):
                names = [n for n in assign_names(s) if n != "__all__"]
                if not names:
                    if "__all__" in assign_names(s):
                        out.append(entry("var", "__all__", assign_code(s, src), ""))
                    continue
                if any(shown(n) for n in names) or any(n in used for n in names):
                    out.append(entry("var", ",".join(names), assign_code(s, src), "",
                                     not any(shown(n) for n in names)))
            elif isinstance(s, ast.ImportFrom) and mod["package"]:
                base = resolve_relative(s.module, s.level, package_of(mod))
                if base is not None and longest_module(base, modules)[0] is not None:
                    out.append(entry("import", "", src.segment(s), ""))
            elif isinstance(s, ast.If):
                if not is_main_guard(s):
                    visit(s.body)
                    visit(s.orelse)
            elif isinstance(s, TRY):
                visit(s.body)
                for h in s.handlers:
                    visit(h.body)
                visit(s.orelse)
                visit(s.finalbody)
    visit(tree.body)
    return out


# ---- imports ----

def package_of(mod):
    """The package a module's relative imports start from."""
    if mod["package"]:
        return mod["name"]
    return mod["name"].rsplit(".", 1)[0] if "." in mod["name"] else ""


def resolve_relative(module, level, pkg):
    """The absolute name of `from <level dots><module> import ...` in package pkg; None when it leaves it."""
    if not level:
        return module
    if not pkg:
        return None
    parts = pkg.split(".")
    up = level - 1
    if up >= len(parts):
        return None
    base = ".".join(parts[:len(parts) - up])
    return base + "." + module if module else base


def longest_module(dotted, modules):
    """The longest in-scope module dotted starts with, and the names after it."""
    if not dotted:
        return None, None
    parts = dotted.split(".")
    for i in range(len(parts), 0, -1):
        m = ".".join(parts[:i])
        if m in modules:
            return m, parts[i:]
    return None, None


def module_imports(tree, mod, modules):
    deps = set()
    pkg = package_of(mod)
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for a in node.names:
                m, _ = longest_module(a.name, modules)
                if m:
                    deps.add(m)
        elif isinstance(node, ast.ImportFrom):
            base = resolve_relative(node.module, node.level, pkg)
            if base is None:
                continue
            for a in node.names:
                full = base + "." + a.name
                if full in modules:
                    deps.add(full)
                    continue
                m, _ = longest_module(base, modules)
                if m:
                    deps.add(m)
    deps.discard(mod["name"])
    return sorted(deps)


class Exports:
    """What each module defines and what it re-exports, to follow a name to the module that defines it."""

    def __init__(self, trees, mods, modules):
        self.modules = modules
        self.defined = {}
        self.reexports = {}  # module -> {local name: (module, name or None for the module itself)}
        self.stars = {}
        for name, tree in trees.items():
            mod = mods[name]
            pkg = package_of(mod)
            defined, rex, stars = set(), {}, []

            def visit(stmts):
                for s in stmts:
                    if isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                        defined.add(s.name)
                    elif isinstance(s, (ast.Assign, ast.AnnAssign)):
                        defined.update(assign_names(s))
                    elif isinstance(s, ast.ImportFrom):
                        base = resolve_relative(s.module, s.level, pkg)
                        if base is None:
                            continue
                        for a in s.names:
                            if a.name == "*":
                                m, rest = longest_module(base, modules)
                                if m and not rest:
                                    stars.append(m)
                                continue
                            full = base + "." + a.name
                            if full in modules:
                                rex[a.asname or a.name] = (full, None)
                                continue
                            m, rest = longest_module(base, modules)
                            if m and not rest:
                                rex[a.asname or a.name] = (m, a.name)
                    elif isinstance(s, ast.If):
                        visit(s.body)
                        visit(s.orelse)
                    elif isinstance(s, TRY):
                        visit(s.body)
                        for h in s.handlers:
                            visit(h.body)
                        visit(s.orelse)
                        visit(s.finalbody)
            visit(tree.body)
            self.defined[name], self.reexports[name], self.stars[name] = defined, rex, stars

    def resolve(self, mod, name, depth=0):
        """(module, name) of the definition behind mod.name; name None when it is the module itself."""
        if depth > 16:
            return mod, name
        sub = mod + "." + name
        if sub in self.modules and name not in self.defined.get(mod, ()):
            return sub, None
        if name in self.defined.get(mod, ()) and name not in self.reexports.get(mod, {}):
            return mod, name
        r = self.reexports.get(mod, {}).get(name)
        if r is not None:
            if r[1] is None:
                return r[0], None
            return self.resolve(r[0], r[1], depth + 1)
        for s in self.stars.get(mod, ()):
            if name in self.defined.get(s, ()) or name in self.reexports.get(s, {}):
                return self.resolve(s, name, depth + 1)
        return mod, name


def test_refs(tree, pkg, modules, exports):
    refs, names = {}, {}
    aliases = {}  # a local name bound to a dotted module path

    def hit(mod, name=None):
        if name is not None:
            mod, name = exports.resolve(mod, name)
        refs[mod] = refs.get(mod, 0) + 1
        if name is not None:
            names.setdefault(mod, set()).add(name)

    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for a in node.names:
                m, _ = longest_module(a.name, modules)
                if a.asname:
                    aliases[a.asname] = a.name
                else:
                    top = a.name.split(".")[0]
                    aliases[top] = top
                if m:
                    hit(m)
        elif isinstance(node, ast.ImportFrom):
            base = resolve_relative(node.module, node.level, pkg)
            if base is None:
                continue
            for a in node.names:
                if a.name == "*":
                    m, _ = longest_module(base, modules)
                    if m:
                        hit(m)
                    continue
                full = base + "." + a.name
                if full in modules:
                    hit(full)
                    aliases[a.asname or a.name] = full
                    continue
                m, rest = longest_module(base, modules)
                if m is None:
                    continue
                if rest:
                    hit(m)
                    continue
                target, tname = exports.resolve(m, a.name)
                if tname is None:
                    aliases[a.asname or a.name] = target
                hit(m, a.name)
    inner = set()
    for node in ast.walk(tree):
        if not isinstance(node, ast.Attribute) or id(node) in inner:
            continue
        attrs = []
        n = node
        while isinstance(n, ast.Attribute):
            attrs.append(n.attr)
            inner.add(id(n.value))
            n = n.value
        if not isinstance(n, ast.Name) or n.id not in aliases:
            continue
        attrs.reverse()
        dotted = aliases[n.id] + "." + ".".join(attrs)
        m, rest = longest_module(dotted, modules)
        if m is None:
            continue
        if rest:
            hit(m, rest[0])
        else:
            hit(m)
    return {"refs": refs, "names": {m: sorted(v) for m, v in names.items()}}


def main():
    req = json.load(sys.stdin)
    errors = []
    mods = {m["name"]: m for m in req.get("modules", [])}
    modules = set(mods)
    trees, srcs = {}, {}
    for name, m in sorted(mods.items()):
        try:
            src = Source(m["source"])
            trees[name] = ast.parse(src.data, filename=m["path"])
            srcs[name] = src
        except (SyntaxError, ValueError) as e:
            errors.append("%s: %s" % (m["path"], e))
    exports = Exports(trees, mods, modules)
    tests, used = {}, {}
    for t in req.get("tests", []):
        try:
            tree = ast.parse(t["source"].encode("utf-8"), filename=t["path"])
        except (SyntaxError, ValueError):
            continue  # a test file this Python cannot read refers to nothing
        r = test_refs(tree, t.get("package") or "", modules, exports)
        tests[t["path"]] = r
        for m, ns in r["names"].items():
            used.setdefault(m, set()).update(ns)
    out = {}
    for name, tree in trees.items():
        src, mod = srcs[name], mods[name]
        try:
            stub = stub_module(tree, src, mod["path"])
        except (SyntaxError, ValueError) as e:
            errors.append("%s: the stub is not valid Python: %s" % (mod["path"], e))
            continue
        out[name] = {
            "stub": stub,
            "doc": ast.get_docstring(tree) or "",
            "api": module_api(tree, src, mod, modules, used.get(name, set())),
            "imports": module_imports(tree, mod, modules),
        }
    json.dump({"modules": out, "tests": tests, "errors": errors}, sys.stdout)


if __name__ == "__main__":
    main()
