package goucl_test

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/derekmarcotte/goucl"
)

// Parse a configuration and read a few values out of it.
func Example() {
	const src = `
# the public listener
server {
    host    = "example.org";
    port    = 8080;
    timeout = 10s;
    tls     = true;
}
`
	doc, err := goucl.ParseString(src)
	if err != nil {
		log.Fatal(err)
	}

	host, _ := doc.String("server.host")
	port, _ := doc.Int("server.port")
	timeout, _ := doc.Duration("server.timeout")
	tls, _ := doc.Bool("server.tls")

	fmt.Printf("%s:%d tls=%v timeout=%v\n", host, port, tls, timeout)
	// Output:
	// example.org:8080 tls=true timeout=10s
}

func ExampleParseFile() {
	dir, err := os.MkdirTemp("", "goucl")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, "app.conf")
	conf := "name = \"widget\";\nworkers = 4;\n"
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		log.Fatal(err)
	}

	doc, err := goucl.ParseFile(path)
	if err != nil {
		log.Fatal(err)
	}

	name, _ := doc.String("name")
	workers, _ := doc.Int("workers")
	fmt.Println(name, workers)
	// Output:
	// widget 4
}

// Get resolves a dot-separated path. Path segments index arrays by number, so
// one path can descend through objects and arrays alike.
func ExampleNode_Get() {
	doc, err := goucl.ParseString(`
		servers = [
			{ host = "a.example.org"; port = 80; },
			{ host = "b.example.org"; port = 443; },
		]
	`)
	if err != nil {
		log.Fatal(err)
	}

	host, _ := doc.String("servers.1.host")
	fmt.Println(host)

	// Get reports whether the path resolved, so a missing key is not a
	// silently empty node.
	if _, ok := doc.Get("servers.9"); !ok {
		fmt.Println("no tenth server")
	}

	// An empty path is the node itself, which is what lets the typed
	// accessors read a node with or without a path.
	first, _ := doc.Get("servers.0")
	port, _ := first.Int("port")
	fmt.Println(first.Kind(), port)
	// Output:
	// b.example.org
	// no tenth server
	// object 80
}

// The typed accessors report whether they found a value of a usable kind. That
// matters because a missing key, a key of the wrong kind, and a real zero are
// three different answers, and all three occur in configuration.
func ExampleNode_Int() {
	doc, err := goucl.ParseString(`retries = 0; hostname = "db.internal";`)
	if err != nil {
		log.Fatal(err)
	}

	v, ok := doc.Int("retries")
	fmt.Println(v, ok) // the document said 0

	v, ok = doc.Int("absent")
	fmt.Println(v, ok) // no such key

	v, ok = doc.Int("hostname")
	fmt.Println(v, ok) // present, but a string
	// Output:
	// 0 true
	// 0 false
	// 0 false
}

// Duration reads a UCL time value. A bare number is an integer, not seconds,
// so reading one as a duration would invent a unit the document did not write.
func ExampleNode_Duration() {
	doc, err := goucl.ParseString(`timeout = 10s; grace = 2min; plain = 30;`)
	if err != nil {
		log.Fatal(err)
	}

	timeout, _ := doc.Duration("timeout")
	grace, _ := doc.Duration("grace")
	fmt.Println(timeout, grace)

	d, ok := doc.Duration("plain")
	fmt.Println(d, ok)
	// Output:
	// 10s 2m0s
	// 0s false
}

// All walks an object's keys in the order the document wrote them.
func ExampleNode_All() {
	doc, err := goucl.ParseString(`
		name    = "widget";
		workers = 4;
		debug   = false;
	`)
	if err != nil {
		log.Fatal(err)
	}

	for key, v := range doc.All() {
		fmt.Printf("%s: %s\n", key, v.Kind())
	}
	// Output:
	// name: string
	// workers: integer
	// debug: boolean
}

// Values yields every value a key holds, and Elements looks inside an array.
// They answer different questions and neither guesses which was meant: a key
// whose value is an array yields that array from Values, not its contents.
func ExampleNode_Values() {
	doc, err := goucl.ParseString(`
		ports = [80, 443, 8080];

		listen = "eth0";
		listen = "eth1";
	`)
	if err != nil {
		log.Fatal(err)
	}

	// One value, which is an array: Elements looks inside it.
	node, _ := doc.Get("ports")
	var ports []int64
	for p := range node.Elements() {
		n, _ := p.Int("")
		ports = append(ports, n)
	}
	fmt.Println(ports)

	// Two values: Values yields each of them.
	node, _ = doc.Get("listen")
	var ifaces []string
	for v := range node.Values() {
		s, _ := v.String("")
		ifaces = append(ifaces, s)
	}
	fmt.Println(ifaces)

	// Keeping them apart is what makes a repeated key of arrays reachable.
	doc, _ = goucl.ParseString(`allow = [10]; allow = [20];`)
	node, _ = doc.Get("allow")
	for v := range node.Values() {
		for e := range v.Elements() {
			n, _ := e.Int("")
			fmt.Print(n, " ")
		}
	}
	fmt.Println()
	// Output:
	// [80 443 8080]
	// [eth0 eth1]
	// 10 20
}

// JSON is a parse and an encode.
func ExampleNode_Bytes() {
	doc, err := goucl.ParseString(`
		name = "widget";
		limits { memory = 1kb; }
	`)
	if err != nil {
		log.Fatal(err)
	}

	out, err := doc.Bytes(goucl.FormatJSONCompact)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(out))

	// Any node encodes, not only the document root.
	limits, _ := doc.Get("limits")
	out, _ = limits.Bytes(goucl.FormatJSONCompact)
	fmt.Println(string(out))
	// Output:
	// {"name":"widget","limits":{"memory":1024}}
	// {"memory":1024}
}

// Encode writes to an io.Writer rather than allocating a slice.
func ExampleNode_Encode() {
	doc, err := goucl.ParseString(`name = "widget"; workers = 4;`)
	if err != nil {
		log.Fatal(err)
	}
	if err := doc.Encode(os.Stdout, goucl.FormatJSON); err != nil {
		log.Fatal(err)
	}
	// Output:
	// {
	//     "name": "widget",
	//     "workers": 4
	// }
}

// Every parse failure returns a *SyntaxError, so errors.As always succeeds on
// one. Excerpt quotes the offending line with a caret under the column.
func ExampleSyntaxError_Excerpt() {
	_, err := goucl.ParseString("a = 1;\nb = ;\n")

	var se *goucl.SyntaxError
	if errors.As(err, &se) {
		// Column is 0-based, matching the engine's message text.
		fmt.Printf("line %d, column %d (%s)\n", se.Line, se.Column, se.Code)
		fmt.Print(se.Excerpt())
	}
	// Output:
	// line 2, column 4 (syntax)
	// 2 | b = ;
	//   |     ^
}

// Before returns the comment block written above a node.
//
// UCL has no line comments. The parser accumulates comments until the next
// node is built, so a comment trailing a value on the same line belongs to
// whatever node comes next, not to the value it sits beside.
func ExampleNode_Before() {
	doc, err := goucl.ParseString(`
# how many workers to run
# 0 means one per CPU
workers = 4; # this one is not about workers
debug = false;
`)
	if err != nil {
		log.Fatal(err)
	}

	workers, _ := doc.Get("workers")
	for _, line := range workers.Before() {
		fmt.Println(line)
	}

	fmt.Println("--")

	debug, _ := doc.Get("debug")
	for _, line := range debug.Before() {
		fmt.Println(line)
	}
	// Output:
	// # how many workers to run
	// # 0 means one per CPU
	// --
	// # this one is not about workers
}

// Only a comment with nothing following it -- at the end of a document or a
// container -- lands after a node.
//
// Comments survive an encode through FormatConfig alone, because that is the
// only format the C emits them in.
func ExampleNode_After() {
	doc, err := goucl.ParseString("workers = 4;\n# nothing follows this\n")
	if err != nil {
		log.Fatal(err)
	}

	workers, _ := doc.Get("workers")
	fmt.Println(workers.After())

	out, _ := doc.Bytes(goucl.FormatConfig)
	fmt.Print(string(out))
	// Output:
	// [# nothing follows this]
	// workers = 4;
	// # nothing follows this
}
