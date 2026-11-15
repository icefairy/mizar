package engine

import "reflect"

// wrapBudget 用反射给任意签名宿主函数套一层「预算检查 + 错误短路」。
//
// 目标：让 hook 回调里成千上万次的宿主调用（网络/文件/DB）在第 N 次就被拦下，
// 而不是靠 30s 超时（每次调用都很快，超时挡不住「次数放大」）。
//
// 约定：fn 必须是函数；返回 (T) 或 (T, error)。
//   - 有 error 返回值：超预算时返回零值 + error（调用方能感知）
//   - 无 error 返回值：超预算时返回零值（尽力而为，因为签名给不了错误通道）
func wrapBudget(e *Engine, name string, fn any) any {
	if e == nil || fn == nil {
		return fn
	}
	v := reflect.ValueOf(fn)
	t := v.Type()
	if t.Kind() != reflect.Func {
		return fn
	}
	out := t.NumOut()
	hasErr := out > 0 && t.Out(out-1).Implements(reflect.TypeOf((*error)(nil)).Elem())

	// 构造同签名函数（Go 里用 reflect.MakeFunc）
	fnType := t
	made := reflect.MakeFunc(fnType, func(args []reflect.Value) []reflect.Value {
		// 动态读取：允许 New() 之后再用 SetHostBudget/hook_budget() 收紧
		b := e.hostBudget
		if b != nil {
			if err := b.check(name); err != nil {
				res := make([]reflect.Value, out)
				for i := 0; i < out; i++ {
					res[i] = reflect.Zero(t.Out(i))
				}
				if hasErr {
					res[out-1] = reflect.ValueOf(&err).Elem()
				}
				return res
			}
		}
		return v.Call(args)
	})
	return made.Interface()
}
