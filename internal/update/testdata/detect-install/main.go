package main

import (
	"fmt"

	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

func main() {
	fmt.Println(updatepkg.DetectInstallMethod())
}
