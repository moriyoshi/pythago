/* The narration is the source of truth for both the player and subtitle export. */
window.PYTHAGO_STORY = [
 {start:0,end:25,kicker:'The idea',title:'Two runtimes.\nOne process.',sub:'Bring Python into a Go program, without cgo.',footer:'The idea',diagram:'intro',cues:[
  [0,7,'Go is great at building and deploying services. Python has a vast ecosystem of libraries and applications.'],
  [7,14,'What if they could work together inside the same program?'],
  [14,25,'Pythago embeds CPython in Go executables built with CGO disabled, so both languages can call each other in one process.'] ]},
 {start:25,end:65,kicker:'Build time',title:'From Python\nto a Go binary.',sub:'Generate the image once for each Python version, variant and target.',footer:'Build time',diagram:'build',cues:[
  [25,34,'It starts with a Python distribution from python-build-standalone.'],
  [34,43,'Pythago-dist fetches the distribution. On Linux and macOS, it uses bao to translate the Python runtime into a linkable image.'],
  [43,53,'On Windows, the package bundles Python and glue DLLs. At startup, Pythago extracts them to a cache and loads them.'],
  [53,65,'Your Go application imports the generated package. It includes the runtime for the target platform and the standard library. System libraries are still required.'] ]},
 {start:65,end:105,kicker:'The bridge',title:'A conversation\nacross languages.',sub:'The py API connects Go, a small layer of glue, and CPython.',footer:'Runtime',diagram:'bridge',cues:[
  [65,74,'Inside the executable, the py package manages the embedded interpreter and its Python objects.'],
  [74,84,'A Go program can import a Python module, call its functions, and work with the returned values.'],
  [84,94,'Calls cross a small layer of C glue that handles the parts of CPython’s interface Go cannot call directly.'],
  [94,105,'The conversation goes both ways: Python can call Go functions too, and errors can cross the boundary.'] ]},
 {start:105,end:140,kicker:'Python packages',title:'Choose where\nyour code lives.',sub:'Embed small applications; keep large environments on disk.',footer:'Packages',diagram:'packages',cues:[
  [105,114,'Python needs more than an interpreter. It also needs modules, resources, and sometimes native extensions.'],
  [114,123,'The standard library and application files can be served from a Go filesystem, including embedded files.'],
  [123,132,'For larger dependencies, Pythago can use an environment on disk, with compatible packages and native extensions.'],
  [132,140,'Extension support depends on the operating system and the Python version and ABI.'] ]},
 {start:140,end:180,kicker:'Threading',title:'Two schedulers.\nA careful handshake.',sub:'Go concurrency meets CPython’s thread-state rules.',footer:'Threading',diagram:'threads',cues:[
  [140,150,'Go uses goroutines; CPython has its own rules about threads and interpreter state.'],
  [150,160,'Pythago coordinates entry into Python and attaches the appropriate Python thread state to the operating system thread.'],
  [160,170,'On Linux and macOS, eligible Python threads run on goroutines locked to OS threads. On Windows, CPython starts its own threads.'],
  [170,180,'You choose a normal GIL build or an opt-in free-threaded Python build when generating the image.'] ]},
 {start:180,end:245,kicker:'A request',title:'A web request,\nend to end.',sub:'Go handles HTTP. A Python application produces the response.',footer:'Web request',diagram:'request',cues:[
  [180,190,'Now follow a request to a Python web application running inside a Go server.'],
  [190,200,'Go’s net/http accepts the request and passes it to Pythago’s ASGI handler.'],
  [200,211,'The handler translates the request into ASGI events and calls the Python application.'],
  [211,222,'For asynchronous work, Pythago’s aio package provides a Go-backed asyncio event loop.'],
  [222,234,'The application sends its response through ASGI, and Go writes it back to the client.'],
  [234,245,'For synchronous Python web applications, Pythago also provides a WSGI handler.'] ]},
 {start:245,end:280,kicker:'Deployment',title:'One binary.\nOr an environment.',sub:'Use the packaging model that fits your application.',footer:'Deployment',diagram:'deploy',cues:[
  [245,254,'A small pure-Python application can package its Python files and standard library with the executable.'],
  [254,263,'Larger native stacks can instead use an environment on disk beside the application.'],
  [263,272,'Pythago supports Linux, macOS, and Windows on amd64 and arm64.'],
  [272,280,'Compatible native extensions can load from disk. Match the package to your operating system, architecture, and Python ABI.'] ]},
 {start:280,end:300,kicker:'The big picture',title:'Go and Python,\nworking together.',sub:'One process. Familiar ecosystems. A new way to build.',footer:'Pythago',diagram:'outro',cues:[
  [280,289,'Pythago brings CPython into a Go executable, connects the two languages, and lets them share a process.'],
  [289,300,'Use Python’s ecosystem alongside Go’s networking and deployment model. Explore Pythago to see how the pieces fit together.'] ]}
];
