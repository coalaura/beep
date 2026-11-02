package main

import (
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("beep version %s\n", version)

		return
	}

	err := beep()
	if err != nil {
		panic(err)
	}
}
