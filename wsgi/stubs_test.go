package wsgi

import (
	"strings"
	"testing"

	"github.com/moriyoshi/pythago/py"
	pytyping "github.com/moriyoshi/pythago/typing"
)

// TestStubs checks the types-pythago stubs (typing/) of the package's modules against the
// modules: their names, and the parameters of their Python functions (typing/stubcheck.py).
func TestStubs(t *testing.T) {
	var problems []string
	err := py.Do(func() (err error) {
		if err := load(); err != nil { // makes the modules
			return err
		}
		problems, err = pytyping.Check("pythago_wsgi", "_pythago_wsgi")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) > 0 {
		t.Errorf("the stubs and the modules differ:\n%s", strings.Join(problems, "\n"))
	}
}
