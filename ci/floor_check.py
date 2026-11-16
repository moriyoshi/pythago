# Run by ci/floor.sh on old glibc releases: the standard library's native modules and
# threads must work.
import bz2, ctypes, decimal, hashlib, json, lzma, sqlite3, ssl, sys, threading, zlib

done: list[int] = []
t = threading.Thread(target=lambda: done.append(sum(range(1000))))
t.start()
t.join()
assert done == [499500]
db = sqlite3.connect(":memory:")
assert db.execute("select 6 * 7").fetchone() == (42,)
assert hashlib.sha256(b"x").hexdigest().startswith("2d711642")
assert zlib.decompress(zlib.compress(b"abc")) == b"abc"
assert lzma.decompress(lzma.compress(b"abc")) == b"abc"
assert bz2.decompress(bz2.compress(b"abc")) == b"abc"
ssl.create_default_context()
assert ctypes.sizeof(ctypes.c_void_p) == 8
print("floor ok", sys.version.split()[0], ssl.OPENSSL_VERSION)
