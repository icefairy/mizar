// Package builtins 缺失 json/parsing 辅助——复用 standard library
package builtins

import (
	"encoding/json"
)

// jsonUnmarshalSafe 安全反序列化。
func jsonUnmarshalSafe(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}
