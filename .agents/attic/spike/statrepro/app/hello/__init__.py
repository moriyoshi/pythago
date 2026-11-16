"""Test package served to Python from a Go embed.FS."""
import pkgutil


def greeting():
    return pkgutil.get_data(__name__, "data.txt").decode().strip()


def fail():
    raise ValueError("raised from embedded source")
