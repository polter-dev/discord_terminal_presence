package presence

import (
	"fmt"
	"path/filepath"
	"strings"
)

var knownDiscordServerImageNames = map[string]struct{}{
	"discord.exe":            {},
	"discordptb.exe":         {},
	"discordcanary.exe":      {},
	"discorddevelopment.exe": {},
}

func verifyDiscordServerImage(imageName string, overrideSet bool, nameErr error) error {
	if overrideSet || nameErr != nil || imageName == "" {
		return nil
	}

	base := filepath.Base(strings.ReplaceAll(imageName, `\`, string(filepath.Separator)))
	if _, ok := knownDiscordServerImageNames[strings.ToLower(base)]; ok {
		return nil
	}
	return fmt.Errorf("presence: named-pipe server executable %q is not a known Discord executable", imageName)
}
