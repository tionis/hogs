package agent

import (
	"encoding/json"
	"fmt"
)

type GenericResultData struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func (r *GenericResultData) UnmarshalJSON(payload []byte) error {
	type response GenericResultData
	var decoded response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return err
	}
	if !decoded.Success && decoded.Error == "" {
		return fmt.Errorf("failed agent response omitted an error")
	}
	*r = GenericResultData(decoded)
	return nil
}
