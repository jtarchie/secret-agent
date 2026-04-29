package main

import (
	"fmt"

	"github.com/jtarchie/secret-agent/internal/bot"
)

// ListBuiltinsCmd prints every sub-agent template embedded in the binary.
type ListBuiltinsCmd struct{}

func (c *ListBuiltinsCmd) Run() error {
	items, err := bot.ListBuiltins()
	if err != nil {
		return fmt.Errorf("list builtins: %w", err)
	}
	if len(items) == 0 {
		fmt.Println("(no built-in sub-agents are embedded in this binary)")
		return nil
	}
	for _, it := range items {
		if it.Description == "" {
			fmt.Println(it.Name)
			continue
		}
		fmt.Printf("%s — %s\n", it.Name, it.Description)
	}
	return nil
}
