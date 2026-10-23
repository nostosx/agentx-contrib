package utils

import (
	"fmt"

	"github.com/nostosx/agentx-contrib/provider/internal/marshal"
	"github.com/nostosx/agentx/model"
)

// EncodeBody encodes a provider request body with marshal.WithExtra. An
// encoding failure recurs on every attempt, so its error is marked
// permanent.
func EncodeBody(provider string, body any, extra map[string]any) ([]byte, error) {
	b, err := marshal.WithExtra(body, extra)
	if err != nil {
		return nil, model.Permanent(fmt.Errorf("%s: encode request: %w", provider, err))
	}
	return b, nil
}

// NormalizeToolArguments normalizes the arguments of every tool call in msg
// with marshal.Arguments, once a stream has delivered them completely. It
// returns msg.
func NormalizeToolArguments(msg *model.Message) *model.Message {
	for i := range msg.Parts {
		if tc := msg.Parts[i].ToolCall; tc != nil {
			tc.Arguments = marshal.Arguments(tc.Arguments)
		}
	}
	return msg
}
