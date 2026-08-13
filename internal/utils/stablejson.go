// StableJSON 对 map 类型的 JSON 序列化进行稳定排序。
//
// Go 的 json.Marshal 对 map 的 key 顺序是随机的（因为 Go map 本身无序）。
// 当需要哈希/缓存 key/文件去重时，相同的 map 每次序列化的结果不同，
// 导致缓存失效或哈希不一致。
//
// StableJSON 通过对 map 的 key 排序后序列化，确保相同内容永远产出相同 JSON。
//
// 用法：
//
//	m := map[string]int{"b": 2, "a": 1}
//	s, _ := StableJSON(m)
//	fmt.Println(s) // {"a":1,"b":2}  —— 稳定，可哈希
package utils

import (
	"encoding/json"
	"sort"
	"strings"
)

// StableJSON 对输入进行稳定 JSON 序列化。
// - map 的 key 按字典序排序后序列化
// - slice 保持原序
// - 其他类型直接序列化
func StableJSON(v interface{}) (string, error) {
	// 先序列化为原始 JSON 字符串
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}

	// 解析为 interface{} 以便处理 map
	var data interface{}
	if err := json.Unmarshal(raw, &data); err != nil {
		return "", err
	}

	sorted := sortValue(data)
	out, err := json.Marshal(sorted)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func sortValue(v interface{}) interface{} {
	switch x := v.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sorted := make(map[string]interface{}, len(x))
		for _, k := range keys {
			sorted[k] = sortValue(x[k])
		}
		return sorted
	case []interface{}:
		sorted := make([]interface{}, len(x))
		for i, v := range x {
			sorted[i] = sortValue(v)
		}
		return sorted
	default:
		return v
	}
}
