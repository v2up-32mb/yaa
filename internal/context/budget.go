package context

import (
	"fmt"

	"github.com/v2up-32mb/yaa/internal/config"
)

// Budget 是 Build 内部计算的最终预算。
type Budget struct {
	EffectiveWindow int
	ReservedOutput  int
	Input           int
}

// ResolveContextBudget 根据 config、模型窗口和 Agent 输出上限计算输入预算。
// 属于 Context 包；internal/config 不导入 Provider 包。
func ResolveContextBudget(
	cfg config.ContextConfig,
	modelWindow int,
	modelMaxOutput int,
	outputTokens int,
) (Budget, error) {
	if modelWindow <= 0 || modelMaxOutput <= 0 {
		return Budget{}, fmt.Errorf("%w: model_window=%d model_max_output=%d", ErrProviderWindowUnknown, modelWindow, modelMaxOutput)
	}
	if outputTokens <= 0 || outputTokens > modelMaxOutput {
		return Budget{}, fmt.Errorf("%w: output_tokens=%d model_max_output=%d", ErrContextConfigInvalid, outputTokens, modelMaxOutput)
	}
	window := modelWindow
	if cfg.MaxTokens > 0 && cfg.MaxTokens < window {
		window = cfg.MaxTokens
	}
	if cfg.ReservedTokens < outputTokens || cfg.ReservedTokens >= window {
		return Budget{}, fmt.Errorf("%w: reserved=%d < output=%d or >= window=%d", ErrContextConfigInvalid, cfg.ReservedTokens, outputTokens, window)
	}
	return Budget{
		EffectiveWindow: window,
		ReservedOutput:  cfg.ReservedTokens,
		Input:           window - cfg.ReservedTokens,
	}, nil
}
