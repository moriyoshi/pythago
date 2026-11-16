"""The Python half of examples/hello."""
import json
import sys
from concurrent.futures import ThreadPoolExecutor
from importlib.resources import files

import host  # a Go module (py.Config.Modules)


def greet(name: str) -> str:
    banner = files(__name__).joinpath("banner.txt").read_text().strip()
    with ThreadPoolExecutor(4) as pool:  # Python threads on goroutines
        shouted = list(pool.map(host.shout, [name, "python " + sys.version.split()[0]]))
    return json.dumps({"banner": banner, "greeting": shouted})
