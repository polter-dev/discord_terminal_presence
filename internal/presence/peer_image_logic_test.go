package presence

import (
	"errors"
	"testing"
)

func TestVerifyDiscordServerImage(t *testing.T) {
	tests := []struct {
		name        string
		imageName   string
		overrideSet bool
		nameErr     error
		wantErr     bool
	}{
		{name: "stable", imageName: "Discord.exe"},
		{name: "stable full path", imageName: `C:\Users\me\AppData\Local\Discord\app-1.0\Discord.exe`},
		{name: "ptb", imageName: "DiscordPTB.exe"},
		{name: "canary", imageName: "DiscordCanary.exe"},
		{name: "development", imageName: "DiscordDevelopment.exe"},
		{name: "lowercase", imageName: "discord.exe"},
		{name: "evil", imageName: "evil.exe", wantErr: true},
		{name: "cmd", imageName: "cmd.exe", wantErr: true},
		{name: "empty name"},
		{name: "lookup error", imageName: "evil.exe", nameErr: errors.New("process exited")},
		{name: "override", imageName: "evil.exe", overrideSet: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := verifyDiscordServerImage(tt.imageName, tt.overrideSet, tt.nameErr)
			if tt.wantErr && err == nil {
				t.Fatal("verifyDiscordServerImage returned nil, want error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("verifyDiscordServerImage returned error: %v", err)
			}
		})
	}
}
