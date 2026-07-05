package presence

import richclient "github.com/hugolgst/rich-go/client"

// Client is the Discord IPC boundary. Tests should inject a fake implementation.
type Client interface {
	Login(appID string) error
	SetActivity(Activity) error
	Logout() error
}

// RichClient adapts github.com/hugolgst/rich-go/client to the local Client interface.
type RichClient struct{}

// Login connects to Discord IPC using the public application ID.
func (RichClient) Login(appID string) error {
	return richclient.Login(appID)
}

// SetActivity pushes one activity payload to Discord.
func (RichClient) SetActivity(activity Activity) error {
	return richclient.SetActivity(toRichActivity(activity))
}

// Logout closes the Discord IPC connection.
func (RichClient) Logout() (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = errFromPanic(recovered)
		}
	}()
	richclient.Logout()
	return nil
}

func toRichActivity(activity Activity) richclient.Activity {
	richActivity := richclient.Activity{
		Details:    activity.Details,
		State:      activity.State,
		LargeImage: imageValue(activity.LargeImage),
		LargeText:  activity.LargeImage.Text,
		SmallImage: imageValue(activity.SmallImage),
		SmallText:  activity.SmallImage.Text,
	}

	if activity.StartTimestamp != nil {
		richActivity.Timestamps = &richclient.Timestamps{Start: activity.StartTimestamp}
	}

	if len(activity.Buttons) > 0 {
		richActivity.Buttons = make([]*richclient.Button, 0, len(activity.Buttons))
		for _, button := range activity.Buttons {
			richActivity.Buttons = append(richActivity.Buttons, &richclient.Button{
				Label: button.Label,
				Url:   button.URL,
			})
		}
	}

	return richActivity
}

func imageValue(image Image) string {
	if image.URL != "" {
		return image.URL
	}
	return image.Key
}

func errFromPanic(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return &panicError{value: recovered}
}

type panicError struct {
	value any
}

func (e *panicError) Error() string {
	return "presence: rich-go logout panic"
}
