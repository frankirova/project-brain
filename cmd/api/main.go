package main

import (
	"fmt"
	"os"

	"github.com/frankirova/project-brain/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("project-brain api scaffold ready environment=%s port=%s\n", cfg.Environment, cfg.Port)
}
