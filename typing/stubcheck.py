"""Checks a stub (types-pythago) against the live module, inside pythago: the aio, asgi
and wsgi tests run check(name, stub_source) and fail on what it returns.

- Every name the stub defines exists in the module, except type aliases and type
  variables, and classes marked @type_check_only, which must not exist.
- Every public name the module defines (not a module, not imported from elsewhere, not
  TYPE_CHECKING or a __future__ feature) is in the stub.
- A stub class's methods exist on the class.
- A function or method that is Python code at runtime has the stub's parameters: the same
  names, kinds and defaults (present or not). Go functions have no signature to compare.
"""

import __future__
import ast
import importlib
import inspect
import types

_KINDS = {
    "posonly": inspect.Parameter.POSITIONAL_ONLY,
    "arg": inspect.Parameter.POSITIONAL_OR_KEYWORD,
    "vararg": inspect.Parameter.VAR_POSITIONAL,
    "kwonly": inspect.Parameter.KEYWORD_ONLY,
    "kwarg": inspect.Parameter.VAR_KEYWORD,
}


def _stub_params(fn):
    a = fn.args
    out = []
    pos = a.posonlyargs + a.args
    defaults = [None] * (len(pos) - len(a.defaults)) + list(a.defaults)
    for i, p in enumerate(pos):
        kind = "posonly" if i < len(a.posonlyargs) else "arg"
        out.append((p.arg, _KINDS[kind], defaults[i] is not None))
    if a.vararg:
        out.append((a.vararg.arg, _KINDS["vararg"], False))
    for p, d in zip(a.kwonlyargs, a.kw_defaults):
        out.append((p.arg, _KINDS["kwonly"], d is not None))
    if a.kwarg:
        out.append((a.kwarg.arg, _KINDS["kwarg"], False))
    return out


def _runtime_params(obj):
    sig = inspect.signature(obj)
    return [(p.name, p.kind, p.default is not inspect.Parameter.empty) for p in sig.parameters.values()]


def _decorators(node):
    names = set()
    for d in node.decorator_list:
        if isinstance(d, ast.Name):
            names.add(d.id)
        elif isinstance(d, ast.Attribute):
            names.add(d.attr)
    return names


def _type_only(stmt):
    """A type alias or a type variable: nothing at runtime."""
    if isinstance(stmt, ast.AnnAssign):
        ann = stmt.annotation
        return isinstance(ann, (ast.Name, ast.Attribute)) and getattr(ann, "id", getattr(ann, "attr", "")) == "TypeAlias"
    if isinstance(stmt, ast.Assign) and isinstance(stmt.value, ast.Call):
        f = stmt.value.func
        return getattr(f, "id", getattr(f, "attr", "")) in ("TypeVar", "TypeVarTuple", "ParamSpec", "NewType")
    return False


def _compare(where, stub_fn, obj, problems):
    if not isinstance(obj, (types.FunctionType, types.MethodType)):
        return  # a Go function or a C builtin: no signature
    want, got = _stub_params(stub_fn), _runtime_params(obj)
    if want != got:
        problems.append("%s: the stub has parameters %s, the module %s" % (where, want, got))


def check(name, source):
    mod = importlib.import_module(name)
    tree = ast.parse(source)
    problems = []
    declared = set()
    for stmt in tree.body:
        if isinstance(stmt, (ast.Import, ast.ImportFrom, ast.Expr)):
            continue
        if isinstance(stmt, ast.ClassDef):
            declared.add(stmt.name)
            if "type_check_only" in _decorators(stmt):
                if hasattr(mod, stmt.name):
                    problems.append("%s.%s: marked @type_check_only, but the module has it" % (name, stmt.name))
                continue
            cls = getattr(mod, stmt.name, None)
            if not isinstance(cls, type):
                problems.append("%s.%s: a class in the stub, not in the module" % (name, stmt.name))
                continue
            for item in stmt.body:
                if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    where = "%s.%s.%s" % (name, stmt.name, item.name)
                    if not hasattr(cls, item.name):
                        problems.append("%s: in the stub, not in the module" % where)
                    else:
                        _compare(where, item, cls.__dict__.get(item.name, getattr(cls, item.name)), problems)
            continue
        if isinstance(stmt, (ast.FunctionDef, ast.AsyncFunctionDef)):
            declared.add(stmt.name)
            where = "%s.%s" % (name, stmt.name)
            if not hasattr(mod, stmt.name):
                problems.append("%s: in the stub, not in the module" % where)
            else:
                _compare(where, stmt, getattr(mod, stmt.name), problems)
            continue
        if isinstance(stmt, (ast.Assign, ast.AnnAssign)):
            targets = stmt.targets if isinstance(stmt, ast.Assign) else [stmt.target]
            for t in targets:
                if isinstance(t, ast.Name):
                    declared.add(t.id)
                    if not _type_only(stmt) and not hasattr(mod, t.id):
                        problems.append("%s.%s: in the stub, not in the module" % (name, t.id))
            continue
        problems.append("%s: a stub statement this check does not know: %s" % (name, ast.dump(stmt)[:80]))
    for attr, value in vars(mod).items():
        if attr.startswith("_") or attr == "TYPE_CHECKING" or attr in declared:
            continue
        if isinstance(value, (types.ModuleType, __future__._Feature)):
            continue  # an imported module, or `from __future__ import annotations`
        origin = getattr(value, "__module__", None)
        if isinstance(value, (type, types.FunctionType)) and origin not in (None, name):
            continue  # imported from elsewhere
        problems.append("%s.%s: in the module, not in the stub" % (name, attr))
    return problems
