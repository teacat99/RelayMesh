package main

import (
	"os"

	"github.com/teacat99/RelayMesh/v2/internal/hookcli"
)

var version = "dev"

func init() {
	hookcli.SetBuildVersion(version)
}

func main() {
	os.Exit(hookcli.Main(os.Args))
}
