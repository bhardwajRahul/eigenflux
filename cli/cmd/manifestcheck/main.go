// manifestcheck verifies release records using the same trust root as skills sync.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"cli.eigenflux.ai/internal/skills"
)

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: manifestcheck manifest.json")
	}
	data, err := os.ReadFile(flag.Arg(0))
	if err != nil {
		log.Fatal(err)
	}
	var manifest skills.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		log.Fatal(err)
	}
	if err := skills.ValidateSignedRelease(&manifest); err != nil {
		log.Fatal(err)
	}
	fmt.Println(manifest.Sequence)
}
