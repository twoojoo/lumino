// Command sitegen builds the static website (GitHub Pages) into a directory.
//
//	go run ./scripts/sitegen -out dist/site
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/twoojoo/lumino/internal/site"
)

func main() {
	out := flag.String("out", "dist/site", "output directory (replaced)")
	base := flag.String("url", site.DefaultURL, "public URL of the site, with the trailing slash")
	flag.Parse()
	if err := site.Build(*out, site.Options{BaseURL: *base}); err != nil {
		fmt.Fprintln(os.Stderr, "sitegen:", err)
		os.Exit(1)
	}
}
