package agent

// Clone 复制一个 Agent 实例，用于多会话并发。
//
// 语义：**共享只读/全局服务，隔离运行态**。
//
//   - 共享（直接复用指针）：LLM、Plugins（工具注册表）、System/Soul/SkillsPrompt
//     等提示词素材、Compactor、Tuner、Guard、Jobs、PlanMode、GrillMode、
//     GoalService、SubAgents、BgReview、AuxLLM、Hooks 等。
//     这些都是进程级服务，复制一份会让两边注册表/计数器不一致。
//   - 隔离（重新分配）：Initial（会话历史）、OnLLMStream / OnToolExchange /
//     OnCompact（闭包捕获会话 id）、StatsData、steer 槽位、aborted、
//     runActive、runCancel、currentTask、cacheStats。
//
// 典型用途：Web UI 多会话同时运行 —— 每个会话一个 Clone，
// 事件各自带 conversation_id，互不串台。
func (a *Agent) Clone() *Agent {
	if a == nil {
		return nil
	}
	n := &Agent{
		// ---- 共享的进程级服务 ----
		LLM:          a.LLM,
		Plugins:      a.Plugins,
		System:       a.System,
		Soul:         a.Soul,
		SkillsPrompt: a.SkillsPrompt,
		PluginDir:    a.PluginDir,
		WorkDir:      a.WorkDir,
		MaxSteps:     a.MaxSteps,
		VerboseLog:   a.VerboseLog,
		Compactor:    a.Compactor,
		Hooks:        a.Hooks,
		Tuner:        a.Tuner,
		Guard:        a.Guard,
		Permission:   a.Permission, // 权限门禁进程级共享：模式切换对全部会话/子代理即时生效（GAP-S2）
		Jobs:         a.Jobs,
		PlanMode:     a.PlanMode,
		GrillMode:    a.GrillMode,
		GoalService:  a.GoalService,
		GoalLoop:     a.GoalLoop,
		SubAgents:    a.SubAgents,
		BgReview:     a.BgReview,
		Completeness: a.Completeness,
		AuxLLM:       a.AuxLLM,
		AskUser:      a.AskUser,

		// ---- 运行态：全新，不继承父实例 ----
		Initial: nil,
		// ConvID 由调用方（server.agentPool）在克隆后立即赋值；
		// 这里显式清空，避免从模板继承一个错误的会话归属。
		ConvID:         "",
		StatsData:      &StatsDataRef{},
		callParser:     a.callParser,
		Commands:       a.Commands, // 斜杠命令注册表只读，共享
		cacheStats:     &CacheStats{},
		steer:          nil,
		steerSeq:       0,
		currentTask:    "",
		OnLLMStream:    nil,
		OnToolExchange: nil,
		OnCompact:      nil,
	}
	// aborted / runActive / runCancel 是零值语义（未中止、未运行、无取消函数），
	// 新实例保持零值即可，不能从父实例复制。
	return n
}
