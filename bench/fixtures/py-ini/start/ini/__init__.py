"""A small INI reader. See README.md for the exact rules."""

from .config import Config
from .errors import IniError
from .parser import loads

__all__ = ["Config", "IniError", "loads"]
