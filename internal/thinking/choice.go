package thinking

import modelhubv2 "github.com/wgdl666/wgModelHub/gen/wg_model_hub/v2"

// Choice 是文本 Generate 一次取舍后的思考字段。
// 关优先，所以 Off 时不再保留档位和预算。
// 档位和预算可以同时留下，由各家决定发哪一个，避免两个一起打到上游。
type Choice struct {
	Off    bool
	On     bool
	Level  modelhubv2.ThinkingLevel
	Budget *int32
}

func FromText(text *modelhubv2.TextOutput) Choice {
	if text == nil {
		return Choice{}
	}
	if text.Thinking == modelhubv2.ThinkingMode_THINKING_MODE_DISABLED {
		return Choice{Off: true}
	}
	choice := Choice{On: text.Thinking == modelhubv2.ThinkingMode_THINKING_MODE_ENABLED}
	if text.ThinkingLevel != modelhubv2.ThinkingLevel_THINKING_LEVEL_UNSPECIFIED {
		choice.Level = text.ThinkingLevel
	}
	if text.ThinkingBudget != nil {
		budget := *text.ThinkingBudget
		choice.Budget = &budget
	}
	return choice
}
